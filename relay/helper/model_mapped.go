package helper

import (
	"github.com/QuantumNous/new-api/pkg/modelalias"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}

	// map model name
	modelMapping := c.GetString("model_mapping")
	if modelMapping != "" && modelMapping != "{}" {
		// APIDUO:model-mapping-pure:BEGIN
		// The resolution lives in a function without a gin context because the
		// worker needs it too: when a rate-limited task moves to another
		// channel, the request body has to be re-mapped for that channel, and by
		// then there is no request left — only a stored body and a channel row.
		upstream, mapped, err := modelalias.ApplyModelMapping(modelMapping, info.OriginModelName)
		if err != nil {
			return err
		}
		info.IsModelMapped = mapped
		if mapped {
			info.UpstreamModelName = upstream
		}
		// APIDUO:model-mapping-pure:END
	}

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}
