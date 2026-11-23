package types

import (
	"errors"
	"strings"
	"testing"
)

func TestClientFacingHidesUpstreamModelName(t *testing.T) {
	upstream := OpenAIError{
		Message: "The model `deepseek-v3-250101` does not exist",
		Type:    "invalid_request_error",
		Code:    "model_not_found",
		Param:   "model",
	}
	e := WithOpenAIError(upstream, 404)
	e.SetClientFacing("req_abc")

	got := e.ToOpenAIError()
	if got.Code != ClientCodeModelNotFound {
		t.Fatalf("code = %v, want %s", got.Code, ClientCodeModelNotFound)
	}
	if got.Type != "not_found_error" {
		t.Fatalf("type = %s, want not_found_error", got.Type)
	}
	if strings.Contains(got.Message, "deepseek") {
		t.Fatalf("upstream model leaked in message: %s", got.Message)
	}
	if !strings.Contains(got.Message, "req_abc") {
		t.Fatalf("request id missing: %s", got.Message)
	}
	if got.Param != "model" {
		t.Fatalf("param = %s, want model", got.Param)
	}
}

func TestClientFacingWithoutOverrideKeepsUpstreamText(t *testing.T) {
	upstream := OpenAIError{
		Message: "The model `deepseek-v3-250101` does not exist",
		Type:    "invalid_request_error",
		Code:    "model_not_found",
	}
	e := WithOpenAIError(upstream, 404)
	got := e.ToOpenAIError()
	if !strings.Contains(got.Message, "deepseek-v3-250101") {
		t.Fatalf("baseline behaviour changed: %s", got.Message)
	}
}

func TestClientFacingMapsStatusAndCodes(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		code       string
		typ        string
		wantCode   string
		wantStatus int
	}{
		{name: "context length", status: 400, code: "context_length_exceeded", typ: "invalid_request_error", wantCode: ClientCodeContextLengthExceeded, wantStatus: 400},
		{name: "rate limit", status: 429, code: "rate_limit_exceeded", typ: "rate_limit_error", wantCode: ClientCodeRateLimitExceeded, wantStatus: 429},
		{name: "auth", status: 401, code: "invalid_api_key", typ: "authentication_error", wantCode: ClientCodeInvalidApiKey, wantStatus: 401},
		{name: "server error becomes 502", status: 500, code: "internal", typ: "server_error", wantCode: ClientCodeUpstreamUnavailable, wantStatus: 502},
		{name: "unknown 4xx falls back", status: 418, code: "", typ: "", wantCode: ClientCodeInvalidRequest, wantStatus: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := WithOpenAIError(OpenAIError{Message: "upstream text", Type: tc.typ, Code: tc.code}, tc.status)
			e.SetClientFacing("")
			got := e.ToOpenAIError()
			if got.Code != tc.wantCode {
				t.Fatalf("code = %v, want %s", got.Code, tc.wantCode)
			}
			status, ok := e.ClientErrorStatus()
			if !ok || status != tc.wantStatus {
				t.Fatalf("status = %d (ok=%v), want %d", status, ok, tc.wantStatus)
			}
		})
	}
}

func TestClientFacingKeepsTopUpHint(t *testing.T) {
	e := NewError(errors.New("quota exhausted"), ErrorCodeInsufficientUserQuota)
	e.SetClientFacing("req_xyz")
	got := e.ToOpenAIError()
	if got.Code != ClientCodeInsufficientQuota {
		t.Fatalf("code = %v, want %s", got.Code, ClientCodeInsufficientQuota)
	}
	if got.TopUp == nil {
		t.Fatalf("top-up hint was dropped")
	}
}

func TestClientFacingDropsUnknownParam(t *testing.T) {
	e := WithOpenAIError(OpenAIError{
		Message: "bad value",
		Type:    "invalid_request_error",
		Code:    "invalid_parameter",
		Param:   "vendor_private_field",
	}, 400)
	e.SetClientFacing("")
	got := e.ToOpenAIError()
	if got.Param != "" {
		t.Fatalf("param leaked: %s", got.Param)
	}
}

func TestClientFacingClaudeEnvelope(t *testing.T) {
	e := WithOpenAIError(OpenAIError{
		Message: "model deepseek-v3 is overloaded",
		Type:    "server_error",
		Code:    "server_error",
	}, 500)
	e.SetClientFacing("")
	got := e.ToClaudeError()
	if got.Type != "api_error" {
		t.Fatalf("type = %s, want api_error", got.Type)
	}
	if strings.Contains(got.Message, "deepseek") {
		t.Fatalf("upstream model leaked: %s", got.Message)
	}
}

func TestClientFacingInternalCodesWithoutUpstream(t *testing.T) {
	cases := []struct {
		code       ErrorCode
		wantCode   string
		wantStatus int
	}{
		{ErrorCodeChannelResponseTimeExceeded, ClientCodeUpstreamTimeout, 504},
		{ErrorCodeModelPriceError, ClientCodeInternalError, 500},
		{ErrorCodeGenRelayInfoFailed, ClientCodeInternalError, 500},
		{ErrorCodeChannelNoAvailableKey, ClientCodeUpstreamUnavailable, 502},
		{ErrorCodePromptBlocked, ClientCodeContentFilter, 400},
		{ErrorCodeAccessDenied, ClientCodeModelNotAuthorized, 403},
		{ErrorCodeBadRequestBody, ClientCodeInvalidRequest, 400},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			e := NewError(errors.New("internal detail for admins"), tc.code)
			e.SetClientFacing("req_1")
			got := e.ToOpenAIError()
			if got.Code != tc.wantCode {
				t.Fatalf("code = %v, want %s", got.Code, tc.wantCode)
			}
			status, ok := e.ClientErrorStatus()
			if !ok || status != tc.wantStatus {
				t.Fatalf("status = %d (ok=%v), want %d", status, ok, tc.wantStatus)
			}
			if strings.Contains(got.Message, "internal detail") {
				t.Fatalf("internal detail leaked: %s", got.Message)
			}
		})
	}
}

func TestClientFacingCoversEveryCatalogEntry(t *testing.T) {
	// A catalog entry nobody can reach is dead weight; a reachable code with no
	// entry would fall through to the status table and lose its meaning.
	for code := range clientErrorByCode {
		if _, ok := clientErrorTypes[clientErrorByCode[code]]; !ok {
			t.Fatalf("%s maps to a code with no type", code)
		}
		if _, ok := clientErrorStatuses[clientErrorByCode[code]]; !ok {
			t.Fatalf("%s maps to a code with no status", code)
		}
	}
	for code := range clientErrorTypes {
		if _, ok := clientErrorStatuses[code]; !ok {
			t.Fatalf("%s has a type but no status", code)
		}
	}
}
