package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// tools[] 里给了本协议不支持的类型，是客户端参数错误：必须 400，
// 否则客户端会把同一个坏请求当服务端故障反复重试。
func TestNewConvertRequestFailedErrorTreatsToolTypeAsClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{}

	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "unsupported tool type is a client error",
			err:      errors.New(`tools[0] has unsupported type "web_search" without a native payload`),
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "tool index prefix without payload",
			err:      errors.New("tools[3]: invalid custom payload"),
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "unrelated converter failure stays 500",
			err:      errors.New("upstream converter exploded"),
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := newConvertRequestFailedError(c, info, tc.err)
			if apiErr == nil {
				t.Fatalf("expected an error, got nil")
			}
			if apiErr.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", apiErr.StatusCode, tc.wantCode)
			}
		})
	}
}
