package helper

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// RemapHardeningEnabled reports whether the serving channel opted into hiding
// its mapping result. It deliberately does not require a matched mapping: an
// exhausted balance is rejected before the mapping helper runs, and that error
// must still leave through the gateway error catalog.
//
// ChannelMeta is embedded as a pointer and is only built by InitChannelMeta,
// which handlers call after the quota check. When it is still nil the channel
// settings the distributor wrote to the context are the only source.
func RemapHardeningEnabled(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if info != nil && info.ChannelMeta != nil {
		return info.ChannelMeta.ChannelOtherSettings.RemapHardening
	}
	if c == nil {
		return false
	}
	settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	if !ok {
		return false
	}
	return settings.RemapHardening
}

// RemapResponseRewrite reports whether the response body still carries the
// executed model name. A channel without a matching mapping keeps its body
// untouched, which is why this is narrower than RemapHardeningEnabled.
func RemapResponseRewrite(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil || !info.IsModelMapped {
		return false
	}
	return info.ChannelMeta.ChannelOtherSettings.RemapHardening
}

// RemapModelIdentity returns the executed model name and the name the client
// asked for, in that order. Both are empty when this request was not remapped,
// which is the signal that an upstream message needs no substitution.
//
// ChannelMeta is embedded as a pointer and is nil until InitChannelMeta runs, so
// it must be checked before IsModelMapped: an error raised before the handler
// reaches the channel (an unpriced model, an exhausted balance) never builds it.
func RemapModelIdentity(info *relaycommon.RelayInfo) (upstream, public string) {
	if info == nil || info.ChannelMeta == nil || !info.IsModelMapped {
		return "", ""
	}
	return info.UpstreamModelName, info.OriginModelName
}

// ImageProtocolEnabled reports whether the serving channel has the image protocol
// on. It reads the same two sources as RemapHardeningEnabled: the relay info once
// InitChannelMeta has run, and the distributor's context settings before that.
func ImageProtocolEnabled(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if info != nil && info.ChannelMeta != nil {
		protocol := info.ChannelMeta.ChannelOtherSettings.ImageProtocol
		return protocol != nil && protocol.Enabled
	}
	if c == nil {
		return false
	}
	settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	if !ok {
		return false
	}
	return settings.ImageProtocol != nil && settings.ImageProtocol.Enabled
}

// RetryAfterHint returns the upstream Retry-After value retained on the error,
// or an empty string when the upstream did not send one.
func RetryAfterHint(err *types.NewAPIError) string {
	if err == nil || len(err.Metadata) == 0 {
		return ""
	}
	var metadata struct {
		RetryAfter string `json:"retry_after"`
	}
	if json.Unmarshal(err.Metadata, &metadata) != nil {
		return ""
	}
	return metadata.RetryAfter
}
