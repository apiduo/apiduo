package advancedcustomasync

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"

	"github.com/gin-gonic/gin"
)

const ChannelName = "advanced_custom_async"

// Adaptor drives a vendor task API described entirely by the channel's
// advanced_async configuration. It supports the submit-then-poll shape:
// POST a request, keep the vendor task id, poll a query endpoint until a
// terminal status, then read the result URL out of the reply.
type Adaptor struct {
	info       *relaycommon.RelayInfo
	config     *kitdto.AdvancedAsyncConfig
	submitBody []byte
}

var _ channel.TaskAdaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.info = info
	if info == nil {
		a.config = nil
		return
	}
	a.config = info.ChannelOtherSettings.AdvancedAsync
}

func (a *Adaptor) configOrError() (*kitdto.AdvancedAsyncConfig, error) {
	if a.config == nil || !a.config.IsEnabled() {
		return nil, fmt.Errorf("advanced_async is not configured for this channel")
	}
	if err := a.config.Validate(); err != nil {
		return nil, err
	}
	return a.config, nil
}

// taskConfig reads the async configuration for a persisted task. Polling runs
// without relay info, and one adaptor instance may serve several tasks
// concurrently, so the channel row is read per call instead of cached on the
// adaptor.
func (a *Adaptor) taskConfig(task *model.Task) (*kitdto.AdvancedAsyncConfig, error) {
	if a.config != nil && a.config.IsEnabled() {
		return a.config, nil
	}
	if task == nil {
		return nil, fmt.Errorf("advanced_async is not configured for this channel")
	}
	channel, err := model.CacheGetChannel(task.ChannelId)
	if err != nil {
		return nil, fmt.Errorf("load channel for advanced_async task: %w", err)
	}
	config := channel.GetOtherSettings().AdvancedAsync
	if config == nil || !config.IsEnabled() {
		return nil, fmt.Errorf("advanced_async is not configured for this channel")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// ValidateRequestAndSetAction reads the client request once, keeps the bytes
// for the upstream submit, and restores the body so later handlers can read it.
func (a *Adaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	if _, err := a.configOrError(); err != nil {
		return taskError(err, "advanced_async_invalid", http.StatusBadRequest)
	}

	body, err := readAndRestoreRequestBody(c)
	if err != nil {
		return taskError(err, "invalid_request", http.StatusBadRequest)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return taskError(fmt.Errorf("request body is required"), "invalid_request", http.StatusBadRequest)
	}
	// 方舟的 callback_url 指向客户端，网关却把上游任务号换成了自己的。
	// 回调送到客户端手里的号查不到任何任务，客户端只会以为回调丢了；
	// 与 doubao 插件一致，直接拒绝，让客户端改用轮询。
	if a.config.NormalizedProtocol() == kitdto.AdvancedAsyncProtocolArk {
		var probe struct {
			CallbackURL *string `json:"callback_url"`
		}
		if err := common.Unmarshal(body, &probe); err == nil && probe.CallbackURL != nil {
			return taskError(
				fmt.Errorf("callback_url is not supported; poll the task instead"),
				"invalid_request", http.StatusBadRequest,
			)
		}
	}
	// 渠道配了模型重定向时，上游请求体里的 model 也要跟着改：这一层原来只改
	// 路由与计价，body 还是客户端那份，上游会拿一个它不认识的模型名把请求拒掉
	// （海螺就是这么拒的：未知 model='hailuo-video-sec'）。没配映射时一个字节不动。
	if info != nil && info.IsModelMapped && strings.TrimSpace(info.UpstreamModelName) != "" {
		if patched, err := ojson.SetBytes(body, "model", info.UpstreamModelName); err == nil {
			body = patched
		}
	}
	a.submitBody = body
	if info != nil && info.Action == "" {
		info.Action = advancedAsyncAction(body)
	}
	return nil
}

// advancedAsyncAction classifies the task from the Ark content parts: an image
// part makes it image-to-video, anything else is text-to-video. A wrong guess
// here mislabels every task log row and every per-action statistic.
func advancedAsyncAction(body []byte) string {
	var payload struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		return constant.TaskActionImageToVideo
	}
	for _, part := range payload.Content {
		switch part.Type {
		case "image_url", "image":
			return constant.TaskActionImageToVideo
		}
	}
	return constant.TaskActionTextToVideo
}

func (a *Adaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	if a.config == nil {
		return nil
	}
	ratios := make(map[string]float64, 2)
	if a.config.Billing.ModeFor(apiduoBillingModelName(info)) == kitdto.AdvancedAsyncBillingPerSecond {
		seconds := a.requestedSeconds()
		if seconds <= 0 {
			seconds = a.config.Billing.DefaultSeconds
		}
		if seconds > 0 {
			ratios["seconds"] = seconds
		}
	}
	// APIDUO:billing-factors:BEGIN A factor carries a price dimension the mode
	// cannot express. Its multiplier joins the same ratio map, so an unmatched
	// or absent value stays at one and never blocks the task.
	if factors := a.config.Billing.Factors; len(factors) > 0 && len(a.submitBody) > 0 {
		var decoded any
		if err := common.Unmarshal(a.submitBody, &decoded); err == nil {
			for _, factor := range factors {
				multiplier, _, _ := factor.MultiplierOf(decoded)
				if multiplier != 1 {
					ratios[factor.Key] = multiplier
				}
			}
		}
	}
	// APIDUO:billing-factors:END
	if len(ratios) == 0 {
		return nil
	}
	return ratios
}

// apiduoBillingModelName is the model name a per-model billing override is
// keyed by. The client spelling wins: it is what the operator wrote in the
// channel settings.
func apiduoBillingModelName(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	if name := strings.TrimSpace(info.OriginModelName); name != "" {
		return name
	}
	return strings.TrimSpace(info.UpstreamModelName)
}

// requestedSeconds reads the client-declared duration from the submit body,
// which validation has kept verbatim for the upstream call.
func (a *Adaptor) requestedSeconds() float64 {
	if len(a.submitBody) == 0 {
		return 0
	}
	path := a.config.Billing.DurationPath
	if len(path) == 0 {
		path = []string{"duration"}
	}
	var decoded any
	if err := common.Unmarshal(a.submitBody, &decoded); err != nil {
		return 0
	}
	raw, ok := kitdto.AdvancedAsyncStringAt(decoded, path)
	if !ok {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return seconds
}

func (a *Adaptor) AdjustBillingOnSubmit(info *relaycommon.RelayInfo, taskData []byte) map[string]float64 {
	return nil
}

// AdjustBillingOnComplete settles a per-token task that came back without
// token counts at the configured fallback price. Returning zero lets the
// poller recalculate from tokens, or leaves the reservation alone when the
// channel sets no fallback price.
func (a *Adaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil {
		return 0
	}
	// Polling builds a fresh adaptor without relay info, so the channel
	// settings have to come off the stored task.
	config, err := a.taskConfig(task)
	if err != nil || config.Billing.TokenFallbackPrice <= 0 {
		return 0
	}
	if taskResult != nil && (taskResult.TotalTokens > 0 || taskResult.CompletionTokens > 0) {
		return 0
	}
	modelName := ""
	groupRatio := 1.0
	if billing := task.PrivateData.BillingContext; billing != nil {
		modelName = billing.OriginModelName
		if billing.GroupRatio > 0 {
			groupRatio = billing.GroupRatio
		}
	}
	if modelName == "" {
		modelName = task.Properties.OriginModelName
	}
	if config.Billing.ModeFor(modelName) != kitdto.AdvancedAsyncBillingPerToken {
		return 0
	}
	quota, _ := common.QuotaFromFloatChecked(config.Billing.TokenFallbackPrice * common.QuotaPerUnit * groupRatio)
	return quota
}

func (a *Adaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	config, err := a.configOrError()
	if err != nil {
		return "", err
	}
	upstreamPath := config.Submit.UpstreamPath
	if info != nil {
		upstreamPath = strings.ReplaceAll(upstreamPath, "{model}", info.UpstreamModelName)
	}
	target, err := resolveTargetURL(channelBaseURL(info), upstreamPath)
	if err != nil {
		return "", err
	}
	return applyQueryAuth(target, config.Submit.Auth, apiKey(info))
}

func (a *Adaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	config, err := a.configOrError()
	if err != nil {
		return err
	}
	if config.Submit.ContentType == kitdto.AdvancedAsyncContentTypeMultipart {
		if contentType := c.GetHeader("Content-Type"); contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	applyHeaderAuth(req.Header, config.Submit.Auth, apiKey(info))
	return nil
}

func (a *Adaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	if _, err := a.configOrError(); err != nil {
		return nil, err
	}
	if len(a.submitBody) == 0 {
		return nil, fmt.Errorf("advanced_async submit body is empty")
	}
	return bytes.NewReader(a.submitBody), nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	config, err := a.configOrError()
	if err != nil {
		return nil, err
	}
	method := config.Submit.Method
	if method == "" {
		method = http.MethodPost
	}
	originalMethod := c.Request.Method
	c.Request.Method = method
	defer func() { c.Request.Method = originalMethod }()
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

// ParseResponse keeps the vendor reply as task data and extracts the id the
// query endpoint needs. A vendor that answers the result inline terminates the
// task immediately instead of leaving it to the poller.
func (a *Adaptor) ParseResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*channel.TaskSubmitResponse, *taskdto.TaskError) {
	config, err := a.configOrError()
	if err != nil {
		return nil, taskError(err, "advanced_async_invalid", http.StatusBadRequest)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, taskError(err, "fail_to_fetch_task", http.StatusBadGateway)
	}
	var decoded any
	if err := common.Unmarshal(body, &decoded); err != nil {
		return nil, taskError(fmt.Errorf("upstream returned invalid JSON: %w", err), "invalid_upstream_response", http.StatusBadGateway)
	}
	taskID, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.TaskIDPath)
	if !ok || taskID == "" {
		return nil, taskError(fmt.Errorf("upstream response has no task id at %s", strings.Join(config.Fields.TaskIDPath, ".")), "invalid_upstream_response", http.StatusBadGateway)
	}
	return &channel.TaskSubmitResponse{
		UpstreamTaskID: taskID,
		TaskData:       body,
	}, nil
}

// FetchTask polls the vendor query endpoint with the configuration's auth.
func (a *Adaptor) FetchTask(baseURL, key string, task *model.Task, proxy string) (*http.Response, error) {
	config, err := a.taskConfig(task)
	if err != nil {
		return nil, err
	}
	taskID := ""
	if task != nil {
		taskID = task.GetUpstreamTaskID()
	}
	if taskID == "" {
		return nil, fmt.Errorf("task has no upstream task id")
	}
	target, err := buildQueryURL(baseURL, config.Query, taskID)
	if err != nil {
		return nil, err
	}
	method := config.Query.Method
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	applyHeaderAuth(request.Header, config.Query.Auth, key)

	client, err := service.GetHttpClientWithProxySettings(proxy, kitdto.ChannelSettings{})
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

// ParseTaskResult maps the vendor reply onto the gateway task state machine.
func (a *Adaptor) ParseTaskResult(task *model.Task, resp *http.Response, respBody []byte) (*relaycommon.TaskInfo, error) {
	config, err := a.taskConfig(task)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := common.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("upstream returned invalid JSON: %w", err)
	}
	rawStatus, _ := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.StatusPath)
	status := config.Fields.NormalizeAdvancedAsyncStatus(rawStatus)

	result := &relaycommon.TaskInfo{Status: status}
	// An empty path resolves to the whole reply, so an unset optional field
	// would otherwise surface the raw JSON as the result URL, progress or
	// failure reason.
	if len(config.Fields.ResultURLPath) > 0 {
		if resultURL, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.ResultURLPath); ok {
			result.Url = resultURL
		}
	}
	// 少数上游（OpenAI 异步图像形状的典型实现：受理回 task_id，未完成回
	// {task_id,status}，完成只回 {data,created,usage}）在终态成功时根本不带
	// status 字段。此时结果本身就是终态证据 —— 没有哪家会给还没跑完的任务
	// 发结果 URL。只在 status **缺失**时兜底；写了但不是已知词的状态仍归
	// UNKNOWN，那是配置或上游改了词，猜成成功会把没做完的活儿结了账。
	if strings.TrimSpace(rawStatus) == "" && result.Url != "" {
		result.Status = "SUCCESS"
	}
	if len(config.Fields.ProgressPath) > 0 {
		if progress, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.ProgressPath); ok {
			result.Progress = progress
		}
	}
	if len(config.Fields.ErrorPath) > 0 {
		if reason, ok := kitdto.AdvancedAsyncStringAt(decoded, config.Fields.ErrorPath); ok {
			result.Reason = reason
		}
	}
	// Token counts let the poller settle a per-token task from the vendor's own
	// numbers; the host recalculates the quota when these are present.
	result.CompletionTokens = advancedAsyncTokensAt(decoded, config.Fields.CompletionTokensPath)
	result.TotalTokens = advancedAsyncTokensAt(decoded, config.Fields.TotalTokensPath)
	if status == "FAILURE" && result.Reason == "" {
		result.Reason = "upstream reported failure"
	}
	return result, nil
}

func (a *Adaptor) GetModelList() []string { return nil }

// advancedAsyncTokensAt reads a token count from an unset-safe path. Vendors
// write it as a JSON number or a numeric string, so both decode through the
// same float parse.
func advancedAsyncTokensAt(root any, path []string) int {
	if len(path) == 0 {
		return 0
	}
	raw, ok := kitdto.AdvancedAsyncStringAt(root, path)
	if !ok {
		return 0
	}
	tokens, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || tokens <= 0 {
		return 0
	}
	return int(tokens)
}

func (a *Adaptor) GetChannelName() string { return ChannelName }

func readAndRestoreRequestBody(c *gin.Context) ([]byte, error) {
	seeker, err := common.GetRequestBody(c)
	if err != nil {
		return nil, err
	}
	reader, ok := seeker.(io.Reader)
	if !ok {
		return nil, fmt.Errorf("request body is not readable")
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return body, nil
}

func channelBaseURL(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	return strings.TrimSpace(info.ChannelBaseUrl)
}

func apiKey(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	return info.ApiKey
}

// resolveTargetURL joins a relative vendor path onto the channel base URL, or
// returns a full URL unchanged.
func resolveTargetURL(baseURL string, target string) (*url.URL, error) {
	trimmed := strings.TrimSpace(target)
	if strings.HasPrefix(trimmed, "/") {
		if strings.HasPrefix(trimmed, "//") {
			return nil, fmt.Errorf("advanced_async upstream path must be a full URL or a path starting with /")
		}
		if baseURL == "" {
			return nil, fmt.Errorf("channel base URL is required when advanced_async upstream path is relative")
		}
		base, err := url.Parse(baseURL)
		if err != nil || base.Scheme == "" || base.Host == "" {
			return nil, fmt.Errorf("channel base URL must be a full URL when advanced_async upstream path is relative")
		}
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return nil, err
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(parsed.Path, "/")
		base.RawPath = ""
		base.RawQuery = parsed.RawQuery
		return base, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("advanced_async upstream path must be a full URL or a path starting with /")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("advanced_async upstream path must use http or https")
	}
	return parsed, nil
}

func buildQueryURL(baseURL string, query kitdto.AdvancedAsyncQuery, taskID string) (string, error) {
	upstreamPath := strings.ReplaceAll(query.UpstreamPath, "{task_id}", url.PathEscape(taskID))
	target, err := resolveTargetURL(baseURL, upstreamPath)
	if err != nil {
		return "", err
	}
	if query.TaskIDLocation != kitdto.AdvancedAsyncTaskIDPath && !strings.Contains(query.UpstreamPath, "{task_id}") {
		values := target.Query()
		values.Set(query.TaskIDParam, taskID)
		target.RawQuery = values.Encode()
	}
	return applyQueryAuth(target, query.Auth, "")
}

func applyQueryAuth(target *url.URL, auth *kitdto.AdvancedCustomRouteAuth, key string) (string, error) {
	if auth == nil || strings.TrimSpace(auth.Type) != kitdto.AdvancedCustomAuthTypeQuery {
		return target.String(), nil
	}
	values := target.Query()
	values.Set(strings.TrimSpace(auth.Name), applyAuthTemplate(auth.Value, key))
	target.RawQuery = values.Encode()
	return target.String(), nil
}

func applyHeaderAuth(header http.Header, auth *kitdto.AdvancedCustomRouteAuth, key string) {
	if auth == nil {
		header.Set("Authorization", "Bearer "+key)
		return
	}
	switch strings.TrimSpace(auth.Type) {
	case kitdto.AdvancedCustomAuthTypeNone, kitdto.AdvancedCustomAuthTypeQuery:
	case kitdto.AdvancedCustomAuthTypeHeader:
		header.Set(strings.TrimSpace(auth.Name), applyAuthTemplate(auth.Value, key))
	default:
		header.Set("Authorization", "Bearer "+key)
	}
}

func applyAuthTemplate(template string, key string) string {
	return strings.ReplaceAll(template, "{api_key}", key)
}

func taskError(err error, code string, status int) *taskdto.TaskError {
	return &taskdto.TaskError{
		Code:       code,
		Message:    err.Error(),
		StatusCode: status,
		Error:      err,
		LocalError: true,
	}
}
