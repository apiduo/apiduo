package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Gemini 入口的错误信封只属于图片渠道：那里的 message 已被冻结成码表文本，
// 上游厂商码（code:"1302"）本来就到不了客户端。普通 Gemini 渠道沿用上游的
// OpenAI 形状，客户端要靠厂商码定位问题 —— 统一套信封会把这条信息吞掉。
func TestGeminiErrorEnvelopeOnlyForImageChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name string
		// image 为 true 时渠道开着图片协议，错误改用 Gemini 信封。
		image bool
	}{
		{name: "protocol off keeps the vendor code"},
		{name: "protocol on renders the gemini envelope", image: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
			settings := dto.ChannelOtherSettings{}
			if tc.image {
				settings.ImageProtocol = &dto.ImageProtocolConfig{
					Enabled:  true,
					Upstream: dto.ImageUpstreamGemini,
				}
			}
			common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, settings)

			// 与 provider 插件路径同形：上游给了自己的错误码。
			apiError := types.WithOpenAIError(types.OpenAIError{
				Message: "too many requests",
				Type:    "rate_limit_error",
				Code:    "1302",
			}, http.StatusTooManyRequests)

			writeRelayStreamError(c, types.RelayFormatGemini, apiError)

			body := recorder.Body.String()
			require.Contains(t, body, "too many requests")
			if tc.image {
				require.Contains(t, body, `"status":"RESOURCE_EXHAUSTED"`)
				require.NotContains(t, body, `"code":"1302"`)
				return
			}
			require.Contains(t, body, `"code":"1302"`)
			require.NotContains(t, body, `"status":`)
		})
	}
}
