package relay

// 目标格式表达不了的客户端参数必须是 400 且不重试。
//
// 这两类错误原先落到 newConvertRequestFailedError 的默认分支，用 NewError
// 的默认状态码 500。客户端（尤其官方 SDK）把 5xx 当服务端故障自动重试，
// 于是一个「删掉 n 或 previous_response_id 就能成功」的请求被反复重打。
//
// 放在独立文件里：上游的 convert_request_error_test.go 已经很长，而这是
// 我方新增的行为契约，独立文件既不用在别人的测试文件里插标记块，合并时
// 也不会冲突。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedTargetFieldsAreBadRequestWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		path   string
		target types.RelayFormat
		build  func() any
	}{
		{
			name:   "chat n>1 转到 responses",
			path:   "/v1/chat/completions",
			target: types.RelayFormatOpenAIResponses,
			build: func() any {
				n := 2
				return &dto.GeneralOpenAIRequest{
					Model:    "m",
					N:        &n,
					Messages: []dto.Message{{Role: "user", Content: "hi"}},
				}
			},
		},
		{
			name:   "responses 状态化字段转到 chat",
			path:   "/v1/responses",
			target: types.RelayFormatOpenAI,
			build: func() any {
				return &dto.OpenAIResponsesRequest{
					Model:              "m",
					Input:              json.RawMessage(`"hi"`),
					PreviousResponseID: "resp_123",
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			info := &relaycommon.RelayInfo{
				OriginModelName: "m",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "m"},
			}

			_, convErr := service.ConvertRequest(c, info, tc.target, tc.build())
			require.Error(t, convErr)

			apiErr := newConvertRequestFailedError(c, info, convErr)
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode, "原始错误: %v", convErr)
			assert.Equal(t, types.ErrorCodeConvertRequestFailed, apiErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(apiErr))
		})
	}
}
