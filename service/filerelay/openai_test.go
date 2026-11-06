package filerelay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecodeUploadedObjectAcceptsBothShapes covers the format split documented
// by Moonshot: the upload reference shows size_bytes / RFC3339 created_at /
// extract_status, while GET /v1/files actually answers the OpenAI shape. An
// upload must not fail because the provider picked the other one.
func TestDecodeUploadedObjectAcceptsBothShapes(t *testing.T) {
	t.Run("openai compatible", func(t *testing.T) {
		object, mime, err := decodeUploadedObject([]byte(`{
			"id": "file-abc123",
			"object": "file",
			"bytes": 2048,
			"created_at": 1735689600,
			"filename": "report.txt",
			"purpose": "file-extract",
			"status": "processed"
		}`))
		require.NoError(t, err)
		assert.Equal(t, "file-abc123", object.ID)
		assert.Equal(t, "file", object.Object)
		assert.EqualValues(t, 2048, object.Bytes)
		assert.EqualValues(t, 1735689600, object.CreatedAt)
		assert.Equal(t, "report.txt", object.Filename)
		assert.Equal(t, "processed", object.Status)
		assert.Empty(t, mime)
	})

	t.Run("moonshot versioned", func(t *testing.T) {
		object, mime, err := decodeUploadedObject([]byte(`{
			"id": "file_01h455vb4pex5vsknk084sn02q",
			"filename": "report.txt",
			"mime_type": "text/plain",
			"size_bytes": 15,
			"created_at": "2026-08-26T12:00:00Z",
			"purpose": "file-extract",
			"extract_status": "ready"
		}`))
		require.NoError(t, err)
		assert.Equal(t, "file_01h455vb4pex5vsknk084sn02q", object.ID)
		assert.Equal(t, "file", object.Object)
		assert.EqualValues(t, 15, object.Bytes)
		assert.EqualValues(t, 1787745600, object.CreatedAt)
		assert.Equal(t, "text/plain", mime)
		assert.Equal(t, "ready", object.Status)
	})

	t.Run("extraction failure is not a success", func(t *testing.T) {
		_, _, err := decodeUploadedObject([]byte(`{
			"id": "file_x",
			"filename": "broken.pdf",
			"size_bytes": 10,
			"extract_status": "error"
		}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to extract")
	})

	t.Run("missing id is rejected", func(t *testing.T) {
		_, _, err := decodeUploadedObject([]byte(`{"filename": "x.txt"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no file id")
	})
}

// TestMoonshotContentEnvelopeIsUnwrapped covers the difference the reference
// page hides: the content endpoint answers application/json with the extracted
// text inside a content field, while the OpenAI contract is a raw byte stream.
func TestMoonshotContentEnvelopeIsUnwrapped(t *testing.T) {
	t.Run("wrapped extraction is unwrapped", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":"# 标题\n\n正文","file_type":"text/plain","filename":"a.md","type":"file"}`))
		}))
		defer server.Close()

		content := fetchTestContent(t, server.URL, "file_x")
		defer content.Body.Close()
		body, err := io.ReadAll(content.Body)
		require.NoError(t, err)
		assert.Equal(t, "# 标题\n\n正文", string(body))
		assert.Equal(t, "text/markdown; charset=utf-8", content.ContentType)
	})

	t.Run("plain json without a content field is left alone", func(t *testing.T) {
		payload := `{"name":"x","value":1}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		}))
		defer server.Close()

		content := fetchTestContent(t, server.URL, "file_x")
		defer content.Body.Close()
		body, err := io.ReadAll(content.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, string(body))
	})
}

func fetchTestContent(t *testing.T, baseURL string, fileID string) *Content {
	t.Helper()
	channel := &model.Channel{Id: 1, Key: "sk-test", Type: constant.ChannelTypeMoonshot}
	base := baseURL
	channel.BaseURL = &base

	backend, err := BackendForChannel(channel)
	require.NoError(t, err)
	content, err := backend.Content(context.Background(), channel, fileID)
	require.NoError(t, err)
	return content
}
