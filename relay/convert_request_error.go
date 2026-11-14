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
	// A request plugin's rejection or failure keeps its own status and code.
	var pluginErr *types.NewAPIError
	if errors.As(err, &pluginErr) && (pluginErr.GetErrorCode() == types.ErrorCodeRequestRejected || pluginErr.GetErrorCode() == types.ErrorCodeRequestPluginFailed) {
		return pluginErr
	}
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
	//
	// 同类的还有目标格式表达不了的客户端参数：chat 的 n>1、responses 的状态化
	// 字段（previous_response_id / conversation / prompt / context_management）。
	// 它们同样是「改请求才能成功」，不是服务端故障。文本取自
	// relaykit/relayconvert 的具体实现，那边改措辞这里要同步；代价是两行判断，
	// 换的是不再让 SDK 把参数错误当 5xx 重试。
	if strings.HasPrefix(err.Error(), "tools[") ||
		strings.Contains(err.Error(), "is not supported in responses compatibility mode") ||
		strings.Contains(err.Error(), "does not support stateful fields:") {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	// APIDUO:tool-type-400:END
	return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
}
