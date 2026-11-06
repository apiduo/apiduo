package filerelay

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

const (
	defaultOpenAIUploadBytes   = int64(512) << 20
	defaultMoonshotUploadBytes = int64(100) << 20
	// maxWrappedContentBytes bounds the read of a JSON-wrapped extraction. A
	// 100 MiB source document expands, but not unboundedly, and buffering is
	// unavoidable because the envelope cannot be streamed without parsing it.
	maxWrappedContentBytes = int64(64) << 20
)

// openAIBackend speaks the /v1/files shape that OpenAI defined and Moonshot
// copied. The two differ only in base URL, upload ceiling, and the meaning of
// the downloaded content: OpenAI returns the original bytes, Moonshot returns
// the extracted markdown for purpose=file-extract.
type openAIBackend struct {
	name string
}

func (b openAIBackend) Name() string { return b.name }

func (b openAIBackend) base(ch *model.Channel) string {
	if b.name == "moonshot" {
		return resolveBase(ch, "https://api.moonshot.cn")
	}
	return resolveBase(ch, "https://api.openai.com")
}

// ceiling resolves the upload limit: the channel's declared override wins,
// otherwise the provider default applies.
func (b openAIBackend) ceiling(override int64) int64 {
	if override > 0 {
		return override
	}
	if b.name == "moonshot" {
		return defaultMoonshotUploadBytes
	}
	return defaultOpenAIUploadBytes
}

func (b openAIBackend) client(ch *model.Channel) *http.Client {
	settings := ch.GetSetting()
	if client, err := service.GetHttpClientWithProxySettings(settings.Proxy, settings); err == nil && client != nil {
		return client
	}
	return service.GetHttpClient()
}

func (b openAIBackend) Upload(ctx context.Context, ch *model.Channel, in *UploadInput) (*UploadResult, error) {
	if in == nil || in.Body == nil {
		return nil, fmt.Errorf("upload requires a file body")
	}
	if limit := b.ceiling(in.MaxBytes); in.Size > limit {
		return nil, &LimitError{Limit: limit, Size: in.Size}
	}

	// The part is streamed into a pipe so a large upload never lands in memory.
	// FormDataContentType is read before the writer goroutine starts: it reads
	// writer state, and racing it with CreateFormFile would be a data race.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()
	go func() {
		part, err := mw.CreateFormFile("file", in.Filename)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, in.Body); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("purpose", in.Purpose); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.CloseWithError(mw.Close())
	}()

	endpoint := joinBase(b.base(ch), "/v1/files")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if err := authorize(req, ch); err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}

	resp, err := b.client(ch).Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload to %s: %w", b.name, err)
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("read %s upload response: %w", b.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamError(resp.StatusCode, body)
	}

	object, upstreamMime, err := decodeUploadedObject(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.name, err)
	}
	if object.Purpose == "" {
		object.Purpose = in.Purpose
	}
	if object.Filename == "" {
		object.Filename = in.Filename
	}

	result := &UploadResult{Object: object, ContentKind: model.FileContentKindBytes, MimeType: upstreamMime}
	// Moonshot's file-extract returns parsed markdown from the content
	// endpoint, and Zhipu's parser returns it inline. Marking the kind here is
	// what lets the download handler set a truthful Content-Type later.
	if b.name == "moonshot" && in.Purpose == PurposeFileExtract {
		result.ContentKind = model.FileContentKindText
	}
	return result, nil
}

func (b openAIBackend) Delete(ctx context.Context, ch *model.Channel, upstreamID string) error {
	if upstreamID == "" {
		return nil
	}
	endpoint := joinBase(b.base(ch), "/v1/files/"+url.PathEscape(upstreamID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	if err := authorize(req, ch); err != nil {
		return err
	}
	resp, err := b.client(ch).Do(req)
	if err != nil {
		return fmt.Errorf("delete on %s: %w", b.name, err)
	}
	defer resp.Body.Close()
	body, readErr := readLimited(resp.Body, 1<<20)
	if resp.StatusCode == http.StatusNotFound {
		// Already gone upstream is a success for our purposes: the row is about
		// to be deleted and the caller asked for the object to stop existing.
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return upstreamError(resp.StatusCode, body)
	}
	if readErr != nil {
		return fmt.Errorf("read %s delete response: %w", b.name, readErr)
	}
	return nil
}

func (b openAIBackend) Content(ctx context.Context, ch *model.Channel, upstreamID string) (*Content, error) {
	if upstreamID == "" {
		return nil, fmt.Errorf("file has no upstream id on this channel")
	}
	endpoint := joinBase(b.base(ch), "/v1/files/"+url.PathEscape(upstreamID)+"/content")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := authorize(req, ch); err != nil {
		return nil, err
	}
	resp, err := b.client(ch).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download from %s: %w", b.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readLimited(resp.Body, 1<<20)
		resp.Body.Close()
		return nil, upstreamError(resp.StatusCode, body)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Moonshot answers the content endpoint with application/json and wraps the
	// extracted text in a {"content": "..."} envelope, while the OpenAI contract
	// this endpoint implements is a raw byte stream. Forwarding the envelope
	// would hand the client a JSON object where it expects Markdown.
	//
	// The unwrap is keyed on the presence of a non-empty content field, so a file
	// that genuinely holds JSON without that key is still returned untouched.
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		body, readErr := readLimited(resp.Body, maxWrappedContentBytes)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read %s content: %w", b.name, readErr)
		}
		var envelope struct {
			Content string `json:"content"`
		}
		if err := common.Unmarshal(body, &envelope); err == nil && envelope.Content != "" {
			payload := []byte(envelope.Content)
			return &Content{
				Body:        io.NopCloser(bytes.NewReader(payload)),
				ContentType: "text/markdown; charset=utf-8",
				Length:      int64(len(payload)),
			}, nil
		}
		return &Content{
			Body:        io.NopCloser(bytes.NewReader(body)),
			ContentType: contentType,
			Length:      int64(len(body)),
		}, nil
	}

	return &Content{
		Body:        resp.Body,
		ContentType: contentType,
		Length:      resp.ContentLength,
	}, nil
}

// fileObjectWire accepts both response shapes the Files API is documented to
// return.
//
// The OpenAI-compatible shape carries bytes, a numeric created_at and status.
// The versioned Kimi shape carries size_bytes, an RFC3339 created_at and
// extract_status, and _GET /v1/files_ actually answers the OpenAI shape today
// even though the reference page shows the versioned one. Parsing both keeps an
// upload from failing on a format the provider may switch to without notice.
type fileObjectWire struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Bytes         int64  `json:"bytes"`
	SizeBytes     int64  `json:"size_bytes"`
	CreatedAt     any    `json:"created_at"`
	Filename      string `json:"filename"`
	Purpose       string `json:"purpose"`
	Status        string `json:"status"`
	ExtractStatus string `json:"extract_status"`
	MimeType      string `json:"mime_type"`
	// Moonshot names the detected type file_type; the reference page shows
	// mime_type. Both are read because the live response uses file_type.
	FileType string `json:"file_type"`
}

// decodeUploadedObject normalises an upload response. The returned MIME type is
// the one the provider detected, which is more trustworthy than a client claim.
func decodeUploadedObject(body []byte) (*Object, string, error) {
	var wire fileObjectWire
	if err := common.Unmarshal(body, &wire); err != nil {
		return nil, "", fmt.Errorf("decode upload response: %w", err)
	}
	if wire.ID == "" {
		return nil, "", fmt.Errorf("provider accepted the upload but returned no file id")
	}
	// A provider that stores the file but fails to extract it still answers 2xx.
	// Surfacing that as a success would hand the client an id whose content
	// endpoint is empty.
	if wire.ExtractStatus == "error" {
		return nil, "", fmt.Errorf("provider failed to extract the file")
	}
	object := &Object{
		ID:        wire.ID,
		Object:    cmp.Or(wire.Object, "file"),
		Bytes:     wire.Bytes,
		Filename:  wire.Filename,
		Purpose:   wire.Purpose,
		Status:    cmp.Or(wire.Status, wire.ExtractStatus),
		CreatedAt: parseCreatedAt(wire.CreatedAt),
	}
	if object.Bytes == 0 {
		object.Bytes = wire.SizeBytes
	}
	return object, cmp.Or(wire.MimeType, wire.FileType), nil
}

// parseCreatedAt accepts a Unix number or an RFC3339 string, because the two
// documented response shapes disagree on the type.
func parseCreatedAt(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case string:
		if parsed, err := time.Parse(time.RFC3339, typed); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

// authorize attaches the channel credential. Moonshot and OpenAI both accept a
// bearer token, and the first enabled key in a multi-key channel is used so a
// disabled key does not fail the upload.
func authorize(req *http.Request, ch *model.Channel) error {
	key, _, apiErr := ch.GetNextEnabledKey()
	if apiErr != nil {
		return fmt.Errorf("channel %d: %s", ch.Id, apiErr.Error())
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("channel %d has no usable key", ch.Id)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return nil
}
