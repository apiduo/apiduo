package types

import (
	"fmt"
	"strconv"
	"strings"
)

// ClientError is the only error identity a client may observe for a request
// whose model was remapped by a channel with remap_hardening enabled.
//
// The catalog is closed: every upstream failure maps to one of these codes.
// A code that is missing here is a gateway bug, not a reason to forward the
// upstream text, because that text names the upstream model.
type ClientError struct {
	Status  int
	Code    string
	Type    string
	Param   string
	Message string
}

// Fixed English is deliberate. API clients branch on Code, and a translated
// message would drift between deployments and make incidents unreproducible.
const (
	ClientCodeInvalidRequest        = "invalid_request"
	ClientCodeContextLengthExceeded = "context_length_exceeded"
	ClientCodeContentFilter         = "content_filter"
	ClientCodeUnsupportedParameter  = "unsupported_parameter"
	ClientCodeInvalidApiKey         = "invalid_api_key"
	ClientCodeModelNotAuthorized    = "model_not_authorized"
	ClientCodeModelNotFound         = "model_not_found"
	ClientCodeRequestTooLarge       = "request_too_large"
	ClientCodeRateLimitExceeded     = "rate_limit_exceeded"
	ClientCodeInsufficientQuota     = "insufficient_quota"
	ClientCodeInternalError         = "internal_error"
	ClientCodeUpstreamUnavailable   = "upstream_unavailable"
	ClientCodeUpstreamTimeout       = "upstream_timeout"
	ClientCodeResponseTooLarge      = "upstream_response_too_large"
)

var clientErrorTypes = map[string]string{
	ClientCodeInvalidRequest:        "invalid_request_error",
	ClientCodeContextLengthExceeded: "invalid_request_error",
	ClientCodeContentFilter:         "invalid_request_error",
	ClientCodeUnsupportedParameter:  "invalid_request_error",
	ClientCodeRequestTooLarge:       "invalid_request_error",
	ClientCodeInvalidApiKey:         "authentication_error",
	ClientCodeModelNotAuthorized:    "permission_error",
	ClientCodeModelNotFound:         "not_found_error",
	ClientCodeRateLimitExceeded:     "rate_limit_error",
	ClientCodeInsufficientQuota:     "rate_limit_error",
	ClientCodeInternalError:         "server_error",
	ClientCodeUpstreamUnavailable:   "server_error",
	ClientCodeUpstreamTimeout:       "server_error",
	ClientCodeResponseTooLarge:      "server_error",
}

var clientErrorStatuses = map[string]int{
	ClientCodeInvalidRequest:        400,
	ClientCodeContextLengthExceeded: 400,
	ClientCodeContentFilter:         400,
	ClientCodeUnsupportedParameter:  400,
	ClientCodeInvalidApiKey:         401,
	ClientCodeModelNotAuthorized:    403,
	ClientCodeModelNotFound:         404,
	ClientCodeRequestTooLarge:       413,
	ClientCodeRateLimitExceeded:     429,
	ClientCodeInsufficientQuota:     429,
	ClientCodeInternalError:         500,
	ClientCodeUpstreamUnavailable:   502,
	ClientCodeUpstreamTimeout:       504,
	ClientCodeResponseTooLarge:      502,
}

// clientErrorByCode holds both gateway codes and the upstream codes that are
// safe to recognise. WithOpenAIError copies an upstream code into errorCode, so
// a single lookup covers both sources.
var clientErrorByCode = map[string]string{
	// gateway codes
	string(ErrorCodeInvalidRequest):                 ClientCodeInvalidRequest,
	string(ErrorCodeSensitiveWordsDetected):         ClientCodeContentFilter,
	string(ErrorCodeRequestRejected):                ClientCodeInvalidRequest,
	string(ErrorCodeRequestPluginFailed):            ClientCodeInvalidRequest,
	string(ErrorCodeResponseRejected):               ClientCodeContentFilter,
	string(ErrorCodeCountTokenFailed):               ClientCodeInvalidRequest,
	string(ErrorCodeModelPriceError):                ClientCodeInternalError,
	string(ErrorCodeInvalidApiType):                 ClientCodeInternalError,
	string(ErrorCodeJsonMarshalFailed):              ClientCodeInternalError,
	string(ErrorCodeDoRequestFailed):                ClientCodeUpstreamUnavailable,
	string(ErrorCodeGetChannelFailed):               ClientCodeUpstreamUnavailable,
	string(ErrorCodeGenRelayInfoFailed):             ClientCodeInternalError,
	string(ErrorCodeChannelNoAvailableKey):          ClientCodeUpstreamUnavailable,
	string(ErrorCodeChannelParamOverrideInvalid):    ClientCodeInvalidRequest,
	string(ErrorCodeChannelHeaderOverrideInvalid):   ClientCodeInvalidRequest,
	string(ErrorCodeChannelModelMappedError):        ClientCodeInternalError,
	string(ErrorCodeChannelAwsClientError):          ClientCodeUpstreamUnavailable,
	string(ErrorCodeChannelInvalidKey):              ClientCodeUpstreamUnavailable,
	string(ErrorCodeChannelResponseTimeExceeded):    ClientCodeUpstreamTimeout,
	string(ErrorCodeReadRequestBodyFailed):          ClientCodeInvalidRequest,
	string(ErrorCodeConvertRequestFailed):           ClientCodeInvalidRequest,
	string(ErrorCodeAccessDenied):                   ClientCodeModelNotAuthorized,
	string(ErrorCodeBadRequestBody):                 ClientCodeInvalidRequest,
	string(ErrorCodeReadResponseBodyFailed):         ClientCodeUpstreamUnavailable,
	string(ErrorCodeBadResponseStatusCode):          ClientCodeUpstreamUnavailable,
	string(ErrorCodeBadResponse):                    ClientCodeUpstreamUnavailable,
	string(ErrorCodeBadResponseBody):                ClientCodeUpstreamUnavailable,
	string(ErrorCodeEmptyResponse):                  ClientCodeUpstreamUnavailable,
	string(ErrorCodeAwsInvokeError):                 ClientCodeUpstreamUnavailable,
	string(ErrorCodeModelNotFound):                  ClientCodeModelNotFound,
	string(ErrorCodePromptBlocked):                  ClientCodeContentFilter,
	string(ErrorCodeInsufficientUserQuota):          ClientCodeInsufficientQuota,
	string(ErrorCodePreConsumeTokenQuotaFailed):     ClientCodeInsufficientQuota,
	// upstream codes worth preserving the meaning of
	"context_length_exceeded":  ClientCodeContextLengthExceeded,
	"content_policy_violation": ClientCodeContentFilter,
	"content_filter":           ClientCodeContentFilter,
	"invalid_parameter":        ClientCodeUnsupportedParameter,
	"unsupported_parameter":    ClientCodeUnsupportedParameter,
	"invalid_api_key":          ClientCodeInvalidApiKey,
	"authentication_error":     ClientCodeInvalidApiKey,
	"permission_denied":        ClientCodeModelNotAuthorized,
	"rate_limit_exceeded":      ClientCodeRateLimitExceeded,
	"insufficient_quota":       ClientCodeInsufficientQuota,
	"request_too_large":        ClientCodeRequestTooLarge,
	"server_error":             ClientCodeUpstreamUnavailable,
	"overloaded_error":         ClientCodeUpstreamUnavailable,
}

var clientErrorByType = map[string]string{
	"invalid_request_error": ClientCodeInvalidRequest,
	"authentication_error":  ClientCodeInvalidApiKey,
	"permission_error":      ClientCodeModelNotAuthorized,
	"not_found_error":       ClientCodeModelNotFound,
	"rate_limit_error":      ClientCodeRateLimitExceeded,
	"server_error":          ClientCodeUpstreamUnavailable,
	"api_error":             ClientCodeUpstreamUnavailable,
	"overloaded_error":      ClientCodeUpstreamUnavailable,
}

var clientErrorByStatus = map[int]string{
	400: ClientCodeInvalidRequest,
	401: ClientCodeInvalidApiKey,
	403: ClientCodeModelNotAuthorized,
	404: ClientCodeModelNotFound,
	413: ClientCodeRequestTooLarge,
	429: ClientCodeRateLimitExceeded,
	500: ClientCodeInternalError,
	502: ClientCodeUpstreamUnavailable,
	503: ClientCodeUpstreamUnavailable,
	504: ClientCodeUpstreamTimeout,
}

// clientErrorParams is the set of parameters a client can act on. Upstream
// parameter names are provider-private and are dropped rather than forwarded.
var clientErrorParams = map[string]struct{}{
	"model":           {},
	"messages":        {},
	"input":           {},
	"max_tokens":      {},
	"temperature":     {},
	"top_p":           {},
	"stop":            {},
	"stream":          {},
	"n":               {},
	"tools":           {},
	"tool_choice":     {},
	"response_format": {},
	"system":          {},
}

func clientErrorMessage(code, requestId string) string {
	message := map[string]string{
		ClientCodeInvalidRequest:        "The request could not be processed.",
		ClientCodeContextLengthExceeded: "The request exceeds the model's context window.",
		ClientCodeContentFilter:         "The request was rejected by the content policy.",
		ClientCodeUnsupportedParameter:  "The request uses a parameter this model does not support.",
		ClientCodeInvalidApiKey:         "The API key is invalid.",
		ClientCodeModelNotAuthorized:    "This key is not authorized for the requested model.",
		ClientCodeModelNotFound:         "The requested model is not available.",
		ClientCodeRequestTooLarge:       "The request is too large.",
		ClientCodeRateLimitExceeded:     "Too many requests. Retry later.",
		ClientCodeInsufficientQuota:     "The account quota is exhausted.",
		ClientCodeInternalError:         "The gateway could not process the request.",
		ClientCodeUpstreamUnavailable:   "The model is temporarily unavailable.",
		ClientCodeUpstreamTimeout:       "The model did not respond in time.",
		ClientCodeResponseTooLarge:      "The model response could not be processed.",
	}[code]
	if message == "" {
		message = "The request could not be processed."
	}
	if requestId == "" {
		return message
	}
	return fmt.Sprintf("%s (request id: %s)", message, requestId)
}

// SetClientFacing freezes the client-visible error identity. The original
// error, its upstream code, and its message stay intact for admin logs.
func (e *NewAPIError) SetClientFacing(requestId string) {
	if e == nil {
		return
	}
	code := e.clientErrorCode()
	e.clientFacing = &ClientError{
		Status:  clientErrorStatuses[code],
		Code:    code,
		Type:    clientErrorTypes[code],
		Param:   e.clientErrorParam(),
		Message: clientErrorMessage(code, requestId),
	}
}

func (e *NewAPIError) clientErrorCode() string {
	if upstream, ok := e.RelayError.(OpenAIError); ok {
		if upstreamCode, ok := upstream.Code.(string); ok && upstreamCode != "" {
			if code, ok := clientErrorByCode[strings.ToLower(upstreamCode)]; ok {
				return code
			}
		}
		if code, ok := clientErrorByType[strings.ToLower(upstream.Type)]; ok {
			return code
		}
	}
	if code, ok := clientErrorByCode[strings.ToLower(string(e.errorCode))]; ok {
		return code
	}
	if code, ok := clientErrorByType[strings.ToLower(string(e.errorType))]; ok {
		return code
	}
	if code, ok := clientErrorByStatus[e.StatusCode]; ok {
		return code
	}
	if e.StatusCode >= 400 && e.StatusCode < 500 {
		return ClientCodeInvalidRequest
	}
	return ClientCodeUpstreamUnavailable
}

func (e *NewAPIError) clientErrorParam() string {
	upstream, ok := e.RelayError.(OpenAIError)
	if !ok || upstream.Param == "" {
		return ""
	}
	if _, allowed := clientErrorParams[upstream.Param]; !allowed {
		return ""
	}
	return upstream.Param
}

// applyClientFacing overwrites only the identity fields, leaving TopUp and any
// other envelope field intact so an actionable hint survives the rewrite.
func (e *NewAPIError) applyClientFacing(target *OpenAIError) {
	if e == nil || e.clientFacing == nil {
		return
	}
	target.Code = e.clientFacing.Code
	target.Type = e.clientFacing.Type
	target.Param = e.clientFacing.Param
	target.Message = e.clientFacing.Message
}

func (e *NewAPIError) applyClientFacingClaude(target *ClaudeError) {
	if e == nil || e.clientFacing == nil {
		return
	}
	target.Type = claudeErrorType(e.clientFacing.Code)
	target.Message = e.clientFacing.Message
}

// claudeErrorType keeps the four categories the Claude SDK understands.
func claudeErrorType(code string) string {
	switch code {
	case ClientCodeInvalidApiKey:
		return "authentication_error"
	case ClientCodeModelNotAuthorized:
		return "permission_error"
	case ClientCodeModelNotFound:
		return "not_found_error"
	case ClientCodeRateLimitExceeded, ClientCodeInsufficientQuota:
		return "rate_limit_error"
	case ClientCodeInternalError, ClientCodeUpstreamUnavailable, ClientCodeUpstreamTimeout, ClientCodeResponseTooLarge:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

// ClientErrorStatus exposes the status a client error resolves to, for callers
// that render outside the two envelope helpers.
func (e *NewAPIError) ClientErrorStatus() (int, bool) {
	if e == nil || e.clientFacing == nil {
		return 0, false
	}
	return e.clientFacing.Status, true
}

// FormatUpstreamCode renders an upstream code of any JSON type for logging.
func FormatUpstreamCode(code any) string {
	switch value := code.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", value)
	}
}
