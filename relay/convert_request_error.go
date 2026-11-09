package relay

import (
	"errors"
	"net/http"
	// APIDUO:tool-type-400:BEGIN
	"strings"
	// APIDUO:tool-type-400:END

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func newConvertRequestFailedError(c *gin.Context, info *relaycommon.RelayInfo, err error) *types.NewAPIError {
	var loss *types.ConversionLossError
	if errors.As(err, &loss) {
		info.RecordConversionDiagnostics(c, loss.Diagnostics)
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if kitreasoning.IsClientError(err) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	// APIDUO:tool-type-400:BEGIN
	// 客户端在 tools[] 里给了本协议不支持的类型（例如 chat 入口带
	// {"type":"web_search"}）是请求参数错误，不是服务端故障。toolconv 只返回普通
	// error，落到下面会成 500，客户端会当可重试的服务端错误一直重打同一个坏请求。
	if strings.HasPrefix(err.Error(), "tools[") {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	// APIDUO:tool-type-400:END
	return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
}
