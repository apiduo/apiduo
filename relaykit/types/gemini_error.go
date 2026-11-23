package types

import "net/http"

// GeminiError is the Gemini protocol error envelope: {error:{code,message,status}}.
type GeminiError struct {
	Error GeminiErrorBody `json:"error"`
}

type GeminiErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// ToGeminiError renders the Gemini envelope. When a client-facing identity has
// been frozen, its fixed message and status win: the upstream's own text often
// names models/…, a vendor host, or nothing the client can act on.
func (e *NewAPIError) ToGeminiError() GeminiError {
	status := http.StatusInternalServerError
	message := ""
	if e != nil {
		if e.StatusCode > 0 {
			status = e.StatusCode
		}
		message = e.Error()
		if e.clientFacing != nil {
			status = e.clientFacing.Status
			message = e.clientFacing.Message
		}
	}
	return GeminiError{Error: GeminiErrorBody{
		Code:    status,
		Message: message,
		Status:  geminiErrorStatus(status),
	}}
}

func geminiErrorStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	}
	if status >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}
