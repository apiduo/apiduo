package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/endpoint"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// APIDUO:local-image-async BEGIN
//
// 同步上游也能服务异步端点：提交时只落一行任务和一个请求快照，由后台
// worker 重放那次同步调用。客户端拿到的是和 8001 完全一样的 task id 与状态机，
// 渠道类型对客户端不可见。
//
// 这条路径只在异步端点上启用，原因见 channelMatchesFilter：把一个普通的同步
// 图像请求变成任务会改变它的生命周期，而客户端并没有要求过。

// apiduoLocalImageAsyncPlatform marks a task the gateway executes itself. It is
// a platform value only for logging and filters; no task adaptor resolves it,
// because there is no upstream task to adapt.
const apiduoLocalImageAsyncPlatform = "local-image-async"

// apiduoLocalImageAsyncMaxRequestBytes bounds the body a local task replays.
//
// The snapshot lives in the task row, so an inline base64 image would put
// megabytes into every row that polls it. URL references stay a few kilobytes;
// anything larger is rejected at submit instead of accepted and then stored.
const apiduoLocalImageAsyncMaxRequestBytes = 256 << 10

// apiduoLocalImageAsyncPlan is the request a local task replays, frozen at
// submit: the body as the client sent it, and the address the accepting channel
// gives its own images. Neither is re-derived later — the channel that was
// selected and the endpoint it publishes are one decision, and splitting that
// decision across two moments is how a task ends up posted somewhere the
// operator never approved.
type apiduoLocalImageAsyncPlan struct {
	// Origin is the client's request before the channel's model mapping. It is
	// what the task stores: preparing per channel at execution is the only way a
	// failover can re-map the body for whichever channel takes the task.
	Origin []byte
	URL    string
}

// apiduoLocalImageAsyncPlanKey carries that plan from the submit attempt to the
// persistence step. It rides the gin context because both run in the same
// request; nothing survives the response.
const apiduoLocalImageAsyncPlanKey = "apiduo_local_image_async_plan"

// apiduoLocalImageAsyncEnabled reads the switch once. Executing a synchronous
// upstream is a capability an operator turns on, not a default: it consumes a
// channel slot for the whole generation and needs a stored request body.
//
// A variable so a test can exercise both answers: the real value is read once
// per process from the environment, and whichever caller touches it first —
// a test or the router — fixes it for the whole run.
var apiduoLocalImageAsyncEnabled = common.LocalImageAsyncEnabled

// apiduoLocalImageAsyncHandles reports whether this attempt is the gateway's own
// job rather than a relayed one.
//
// The switch, the path and the channel type all have to agree. A 8001 channel
// always answers with a task id, so it never takes this path — including on a
// retry, which is what keeps one client request from producing two tasks.
//
// The channel type comes from the request context, never from ChannelMeta: this
// runs before InitChannelMeta, so ChannelMeta still describes the previous
// attempt. A first attempt on a 8001 channel followed by a retry on a
// synchronous one read as "still 8001" and was relayed as an upstream task,
// which is precisely the fallback this feature exists for.
func apiduoLocalImageAsyncHandles(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if !apiduoLocalImageAsyncEnabled() {
		return false
	}
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	channelType := c.GetInt("channel_type")
	if channelType == 0 && info != nil && info.ChannelMeta != nil {
		channelType = info.ChannelMeta.ChannelType
	}
	if channelType == constant.ChannelTypeAdvancedAsync {
		return false
	}
	spec, ok := endpoint.LookupAsync(c.Request.URL.Path)
	return ok && spec.Protocol == constant.ApiduoAsyncProtocolImages
}

// apiduoLocalImageAsyncSubmit prices the call and keeps the body for the worker.
//
// Only per-call pricing is supported. A tiered expression needs the adaptor's
// usage facts, which a synchronous image upstream does not report — pretending
// otherwise would settle from a number nobody produced.
func apiduoLocalImageAsyncSubmit(c *gin.Context, info *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
	if info == nil {
		return nil, service.TaskErrorWrapperLocal(errors.New("relay info is missing"), "invalid_request", http.StatusInternalServerError)
	}
	// The relay path gets this from RelayTaskSubmit; the local path has to ask
	// for it, because the task row records the channel that accepted it and the
	// worker later reads that channel's credential.
	info.InitChannelMeta(c)

	if info.OriginModelName == "" {
		return nil, service.TaskErrorWrapperLocal(errors.New("model is required"), "invalid_request", http.StatusBadRequest)
	}
	info.Action = constant.TaskActionImageGeneration

	// Admission before pricing: a refused submission must not reserve quota, and
	// the refusal has to be the cheapest thing the gateway does. NoRetry because
	// a full queue is global — another channel cannot make room.
	if err := service.AdmitLocalImageAsync(c.GetInt("id")); err != nil {
		taskErr := service.TaskErrorWrapperLocal(err, "queue_full", http.StatusTooManyRequests)
		taskErr.NoRetry = true
		return nil, taskErr
	}

	// The relay path maps the model inside RelayTaskSubmit, which this branch
	// never reaches. The mapping decides both the price and the model name the
	// upstream will read, so it has to run here too — and before the body is
	// frozen, because the frozen body carries that name.
	info.UpstreamModelName = info.OriginModelName
	if err := relayhelper.ModelMappedHelper(c, info, nil); err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_mapping_failed", http.StatusBadRequest)
	}

	priceData, err := relayhelper.ModelPriceHelperPerCall(c, info)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_price_error", http.StatusBadRequest)
	}
	info.PriceData = priceData

	origin, taskErr := apiduoLocalImageAsyncRequestBody(c, info)
	if taskErr != nil {
		return nil, taskErr
	}
	upstreamURL, urlErr := apiduoLocalImageUpstreamURL(info)
	if urlErr != nil {
		// Rejected now rather than stored and failed later: the client is about
		// to be told the work was accepted, and a task whose destination is a
		// guess would fail minutes after that promise.
		return nil, service.TaskErrorWrapperLocal(urlErr, "channel_cannot_serve_images", http.StatusBadGateway)
	}
	c.Set(apiduoLocalImageAsyncPlanKey, &apiduoLocalImageAsyncPlan{Origin: origin, URL: upstreamURL})

	return &relay.TaskSubmitResult{
		Platform: apiduoLocalImageAsyncPlatform,
		Quota:    priceData.Quota,
	}, nil
}

// apiduoLocalImageUpstreamURL answers "where does this channel put a
// synchronous image request", using the two sources the relay itself uses:
//
//  1. the channel's primary endpoint, when it declares one. That field is the
//     operator's statement of the upstream's native endpoint, and both the
//     advanced routes and the image protocol already follow it. Declaring it is
//     how a channel type whose images live somewhere else (a vendor's own
//     generations path) is served without teaching this worker that vendor.
//  2. the adaptor that serves the channel type, asked to build the URL for the
//     Images endpoint. This is the same call the synchronous relay makes, so a
//     channel type whose adaptor maps images elsewhere is followed automatically
//     instead of being hardcoded here.
//
// A channel that answers neither is a configuration error and says so. The
// alternative — falling back to a constant — produces exactly the failure this
// function exists to prevent: a correct-looking task that posts to a path the
// upstream never served.
func apiduoLocalImageUpstreamURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil || info.ChannelMeta == nil {
		return "", errors.New("the selected channel is unknown")
	}
	url := ""
	if primary := kitdto.NormalizePrimaryEndpoint(info.ChannelOtherSettings.PrimaryEndpoint); primary != "" {
		// The operator's declaration outranks any guess. A primary endpoint that
		// is not an image path means the channel is wrong for this task, and the
		// upstream will say so with the address it was given — far easier to
		// diagnose than a request silently sent somewhere nobody configured.
		url = relaycommon.GetFullRequestURL(info.ChannelBaseUrl, primary, info.ChannelType)
	} else {
		adaptor := relay.GetAdaptor(info.ApiType)
		if adaptor == nil {
			return "", fmt.Errorf("channel type %d cannot serve a synchronous image request", info.ChannelType)
		}
		// A copy, including the channel meta: GetRequestURL normalizes the base
		// URL on the value it is handed. Writing that through the shared pointer
		// would change the address the attempt itself reports, which is the value
		// the audit record has to keep.
		probe := *info
		meta := *info.ChannelMeta
		probe.ChannelMeta = &meta
		probe.RelayMode = relayconstant.RelayModeImagesGenerations
		probe.RequestURLPath = endpoint.AsyncImageSyncPath
		adaptor.Init(&probe)
		resolved, err := adaptor.GetRequestURL(&probe)
		if err != nil {
			return "", fmt.Errorf("channel type %d cannot build an images url: %w", info.ChannelType, err)
		}
		url = resolved
	}
	// A channel with no base URL resolves to a bare path, which the worker
	// would fail to send minutes later. Rejecting it here keeps the promise the
	// submit response makes: an address exists and it is absolute.
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("the channel resolved no absolute upstream address (got %q)", url)
	}
	return url, nil
}

// apiduoLocalImageAsyncRequestBody reads the client body once and restores it.
//
// The dispatcher already peeked at it to find the model, so the reader has to be
// seekable — reading the wire body directly would silently return nothing.
//
// The stored copy carries the mapped model name, for the same reason the
// synchronous relay rewrites it: a channel mapping ("alias" → "vendor-model")
// is resolved before the request leaves, and the worker is the only thing that
// will send this one.
func apiduoLocalImageAsyncRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (origin []byte, taskErr *dto.TaskError) {
	seeker, err := common.GetRequestBody(c)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "read_request_body_failed", http.StatusBadRequest)
	}
	reader, ok := seeker.(io.Reader)
	if !ok {
		return nil, service.TaskErrorWrapperLocal(errors.New("request body is not readable"), "read_request_body_failed", http.StatusBadRequest)
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, apiduoLocalImageAsyncMaxRequestBytes+1))
	if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
		return nil, service.TaskErrorWrapperLocal(seekErr, "read_request_body_failed", http.StatusBadRequest)
	}
	if readErr != nil {
		return nil, service.TaskErrorWrapperLocal(readErr, "read_request_body_failed", http.StatusBadRequest)
	}
	if len(raw) > apiduoLocalImageAsyncMaxRequestBytes {
		return nil, service.TaskErrorWrapperLocal(
			errors.New("request body is too large for an asynchronous task; send image references instead of inline data"),
			"request_too_large_for_async", http.StatusRequestEntityTooLarge)
	}
	if !json.Valid(raw) {
		return nil, service.TaskErrorWrapperLocal(errors.New("request body is not JSON"), "invalid_request", http.StatusBadRequest)
	}
	// The body is stored exactly as the client sent it. The channel's model
	// mapping is applied at execution instead, because the task may run on a
	// different channel than the one that accepted it.
	return raw, nil
}

// apiduoLocalImageAsyncWithModel rewrites the one member the mapping changes and
// leaves every other byte's meaning intact.
//
// Decoded with UseNumber: a body carries interleaved integers (seeds, counts,
// ids) that float64 would round, and a silently altered seed is a different
// image request.
func apiduoLocalImageAsyncWithModel(raw []byte, model string) ([]byte, *dto.TaskError) {
	encoded, err := service.RebuildLocalImageBody(raw, model)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "read_request_body_failed", http.StatusBadRequest)
	}
	return encoded, nil
}

// APIDUO:local-image-async END

// apiduoTaskSubmit routes one submission attempt.
//
// The choice is per attempt, not per request: a retry may land on a different
// channel type, and each attempt has to be handled the way its own channel
// answers. A 8001 channel always takes the relay path, so a retry can move from
// upstream-async to local execution but never back.
func apiduoTaskSubmit(c *gin.Context, info *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
	if apiduoLocalImageAsyncHandles(c, info) {
		return apiduoLocalImageAsyncSubmit(c, info)
	}
	return relay.RelayTaskSubmit(c, info)
}

// apiduoLocalImageURLFor resolves one channel's images address without a request
// context.
//
// The worker needs this when it moves a rate-limited task to another channel:
// there is no gin context left, only a channel row. Resolution itself is the
// same function the submission path uses, so the two can never disagree about
// where a channel serves images.
func apiduoLocalImageURLFor(channel *model.Channel, upstreamModel string) (string, error) {
	if channel == nil {
		return "", errors.New("channel is missing")
	}
	apiType, _ := common.ChannelType2APIType(channel.Type)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:          channel.Type,
		ChannelBaseUrl:       channel.GetBaseURL(),
		ApiType:              apiType,
		ChannelOtherSettings: channel.GetOtherSettings(),
		UpstreamModelName:    upstreamModel,
	}}
	return apiduoLocalImageUpstreamURL(info)
}

// The worker lives in service, which cannot import relay (relay imports
// service). Handing it the resolver keeps the resolution logic in one place
// instead of duplicating the adaptor table.
func init() {
	service.LocalImageURLResolver = apiduoLocalImageURLFor
}
