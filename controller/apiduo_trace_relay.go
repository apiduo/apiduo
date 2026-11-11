package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// Bridge between the relay request boundary and the trace collector. This
// file is apiduo-only (new file, never merged from upstream), so
// controller/relay.go's marker blocks stay one line each while this side
// translates *relaycommon.RelayInfo — a type pkg/apiduo/trace must not
// import — into plain fields.

// init hands the build version to the trace package. pkg/apiduo/trace must
// not import common (zero internal imports, docs §5), so the bridge — which
// already lives next to the call sites — is the one place that can read it.
// Package init runs long before the first request, so no span is ever built
// without a release.
func init() {
	trace.SetBuildRelease(common.Version)
}

// apiduoTraceFinalize is called exactly once from Relay()'s defer. It
// covers success, failure, and panic paths: failed requests get traces
// too, which is why finalize lives in the defer rather than at settlement.
//
//go:noinline
func apiduoTraceFinalize(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError, format types.RelayFormat) {
	if info == nil {
		// Request failed before GenRelayInfo (validation/billing). The
		// collector still exists if Begin ran; emit what we know.
		trace.FinalizeRelay(c, trace.RelayMeta{Format: string(format), Success: apiErr == nil})
		return
	}
	meta := trace.RelayMeta{
		UserId:  info.UserId,
		Model:   info.OriginModelName,
		Group:   info.UsingGroup,
		TokenId: info.TokenId,
		// ChannelId is a field promoted through the embedded *ChannelMeta,
		// which is nil until a channel is selected; GetChannelID is the
		// nil-safe accessor (a request that fails before channel selection
		// has no channel).
		ChannelId:     info.GetChannelID(),
		IsStream:      info.IsStream,
		Retry:         info.RetryIndex,
		Format:        string(format),
		Success:       apiErr == nil,
		StartUnixNano: info.StartTime.UnixNano(),
	}
	if info.HasSendResponse() {
		meta.FirstByteNano = info.FirstResponseTime.UnixNano()
	}
	if apiErr != nil {
		meta.ErrorStatus = apiErr.StatusCode
		// MaskSensitiveErrorWithStatusCode strips upstream base URLs and the
		// key material an error body may echo, so the reason is safe to ship
		// even though this span leaves the process.
		meta.ErrorText = apiErr.MaskSensitiveErrorWithStatusCode()
	}
	meta.Operation = apiduoTraceOperation(format)
	trace.FinalizeRelay(c, meta)
}

// apiduoTraceOperation names the Langfuse operation. Conversation formats
// collapse to "chat"; the rest keep their format name so the Langfuse UI can
// filter by kind without a mapping table to maintain.
func apiduoTraceOperation(format types.RelayFormat) string {
	switch format {
	case types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatGemini,
		types.RelayFormatOpenAIResponses:
		return "chat"
	case types.RelayFormatOpenAIImage:
		// An image request is not a conversation: it carries no `messages` and
		// answers with `data[]`. Folding it into "chat" put every image call in
		// the chat bucket — and it made the drawer label an images request
		// "chat flux-3-image" right next to a `{"data":[…]}` body, which reads
		// as "the chat endpoint answered in the wrong shape". It did not; the
		// label was wrong.
		return "image"
	default:
		return string(format)
	}
}
