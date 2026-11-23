package types

import (
	"errors"
	"strings"
	"testing"
)

func TestToGeminiErrorUsesClientFacing(t *testing.T) {
	err := NewErrorWithStatusCode(errors.New("models/gemini-2.5-flash-image is not found"), ErrorCodeInvalidRequest, 400)
	err.SetClientFacing("req-1")

	got := err.ToGeminiError()
	if got.Error.Code != 400 {
		t.Fatalf("code = %d", got.Error.Code)
	}
	if got.Error.Status != "INVALID_ARGUMENT" {
		t.Fatalf("status = %q", got.Error.Status)
	}
	if strings.Contains(got.Error.Message, "gemini") || strings.Contains(got.Error.Message, "models/") {
		t.Fatalf("message leaks upstream: %q", got.Error.Message)
	}
}

func TestToGeminiErrorStatusMapping(t *testing.T) {
	cases := map[int]string{
		400: "INVALID_ARGUMENT",
		401: "UNAUTHENTICATED",
		403: "PERMISSION_DENIED",
		404: "NOT_FOUND",
		429: "RESOURCE_EXHAUSTED",
		503: "UNAVAILABLE",
		500: "INTERNAL",
	}
	for status, want := range cases {
		err := NewErrorWithStatusCode(errors.New("upstream"), ErrorCodeBadResponseBody, status)
		if got := err.ToGeminiError().Error.Status; got != want {
			t.Fatalf("status %d -> %q, want %q", status, got, want)
		}
	}
}
