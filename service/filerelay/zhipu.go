package filerelay

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultZhipuUploadBytes = int64(100) << 20
	// zhipuTokenTTLSeconds mirrors expSeconds in relay/channel/zhipu: the same
	// credential, reused for a different endpoint family.
	zhipuTokenTTLSeconds = 24 * 3600
)

// zhipuBackend speaks the v4 files protocol.
//
// Zhipu splits the two things OpenAI merges: /files stores an object that chat
// later references by file_ids, while /files/parser/sync parses a document and
// answers with text, minting no id at all. purpose=file-extract means "give me
// parsed text", so it takes the parser path and the gateway keeps the result.
type zhipuBackend struct{}

func (zhipuBackend) Name() string { return "zhipu" }

func (zhipuBackend) base(ch *model.Channel) string {
	return resolveBase(ch, "https://open.bigmodel.cn")
}

func (zhipuBackend) client(ch *model.Channel) *http.Client {
	settings := ch.GetSetting()
	if client, err := service.GetHttpClientWithProxySettings(settings.Proxy, settings); err == nil && client != nil {
		return client
	}
	return service.GetHttpClient()
}

func (b zhipuBackend) Upload(ctx context.Context, ch *model.Channel, in *UploadInput) (*UploadResult, error) {
	if in == nil || in.Body == nil {
		return nil, fmt.Errorf("upload requires a file body")
	}
	limit := defaultZhipuUploadBytes
	if in.MaxBytes > 0 {
		limit = in.MaxBytes
	}
	if in.Size > limit {
		return nil, &LimitError{Limit: limit, Size: in.Size}
	}
	if in.Purpose == PurposeFileExtract {
		return b.uploadParsed(ctx, ch, in)
	}
	return b.uploadStored(ctx, ch, in)
}

// uploadStored is the plain object-store path: the provider keeps the bytes and
// the returned id is what chat file_ids expects.
func (b zhipuBackend) uploadStored(ctx context.Context, ch *model.Channel, in *UploadInput) (*UploadResult, error) {
	body, status, err := b.postMultipart(ctx, ch, "/api/paas/v4/files", in, map[string]string{
		"purpose": in.Purpose,
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, upstreamError(status, body)
	}
	object, err := decodeZhipuObject(body)
	if err != nil {
		return nil, err
	}
	if object.Object == "" {
		object.Object = "file"
	}
	if object.Purpose == "" {
		object.Purpose = in.Purpose
	}
	if object.Filename == "" {
		object.Filename = in.Filename
	}
	return &UploadResult{Object: object, ContentKind: model.FileContentKindBytes}, nil
}

// uploadParsed returns text instead of an id. The response shape is not pinned
// down in the published reference, so the payload is read leniently and the raw
// body is kept as a fallback rather than silently returning nothing.
func (b zhipuBackend) uploadParsed(ctx context.Context, ch *model.Channel, in *UploadInput) (*UploadResult, error) {
	body, status, err := b.postMultipart(ctx, ch, "/api/paas/v4/files/parser/sync", in, map[string]string{
		"tool_type": "prime-sync",
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, upstreamError(status, body)
	}
	text := extractParsedText(body)
	if len(text) == 0 {
		return nil, fmt.Errorf("zhipu returned an empty extraction")
	}
	return &UploadResult{
		Object: &Object{
			Object:    "file",
			Bytes:     int64(len(text)),
			CreatedAt: time.Now().Unix(),
			Filename:  in.Filename,
			Purpose:   in.Purpose,
			Status:    "processed",
		},
		Inline:      text,
		ContentKind: model.FileContentKindText,
		MimeType:    "text/markdown",
	}, nil
}

func (b zhipuBackend) postMultipart(ctx context.Context, ch *model.Channel, path string, in *UploadInput, fields map[string]string) ([]byte, int, error) {
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
		for name, value := range fields {
			if err := mw.WriteField(name, value); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		_ = pw.CloseWithError(mw.Close())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinBase(b.base(ch), path), pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	if err := b.authorize(req, ch); err != nil {
		_ = pr.CloseWithError(err)
		return nil, 0, err
	}

	resp, err := b.client(ch).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("upload to zhipu: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, 4<<20)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read zhipu response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func (b zhipuBackend) Delete(ctx context.Context, ch *model.Channel, upstreamID string) error {
	if upstreamID == "" {
		return nil
	}
	endpoint := joinBase(b.base(ch), "/api/paas/v4/files/"+url.PathEscape(upstreamID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	if err := b.authorize(req, ch); err != nil {
		return err
	}
	resp, err := b.client(ch).Do(req)
	if err != nil {
		return fmt.Errorf("delete on zhipu: %w", err)
	}
	defer resp.Body.Close()
	body, _ := readLimited(resp.Body, 1<<20)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return upstreamError(resp.StatusCode, body)
	}
	return nil
}

func (b zhipuBackend) Content(ctx context.Context, ch *model.Channel, upstreamID string) (*Content, error) {
	if upstreamID == "" {
		return nil, fmt.Errorf("file has no upstream id on this channel")
	}
	endpoint := joinBase(b.base(ch), "/api/paas/v4/files/"+url.PathEscape(upstreamID)+"/content")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := b.authorize(req, ch); err != nil {
		return nil, err
	}
	resp, err := b.client(ch).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download from zhipu: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("zhipu keeps no downloadable content for this file; upload with purpose=file-extract to receive parsed text")
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
	return &Content{Body: resp.Body, ContentType: contentType, Length: resp.ContentLength}, nil
}

func (b zhipuBackend) authorize(req *http.Request, ch *model.Channel) error {
	key, _, apiErr := ch.GetNextEnabledKey()
	if apiErr != nil {
		return fmt.Errorf("channel %d: %s", ch.Id, apiErr.Error())
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("channel %d has no usable key", ch.Id)
	}
	token := zhipuJWT(key)
	if token == "" {
		return fmt.Errorf("channel %d has an invalid zhipu key", ch.Id)
	}
	req.Header.Set("Authorization", token)
	return nil
}

// decodeZhipuObject accepts both a bare file object and a {data:{...}} wrapper,
// because the two v4 endpoint families do not document a single envelope.
func decodeZhipuObject(body []byte) (*Object, error) {
	var object Object
	if err := common.Unmarshal(body, &object); err == nil && object.ID != "" {
		return &object, nil
	}
	var envelope struct {
		Data Object `json:"data"`
	}
	if err := common.Unmarshal(body, &envelope); err == nil && envelope.Data.ID != "" {
		return &envelope.Data, nil
	}
	return nil, fmt.Errorf("zhipu accepted the upload but returned no file id: %s", truncateForError(body))
}

// extractParsedText walks the shapes the synchronous parser may answer with and
// falls back to the raw body. Guessing wrong then still yields the provider's
// own text instead of an empty file.
func extractParsedText(body []byte) []byte {
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return body
	}
	for _, key := range []string{"content", "text", "data", "result"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if typed != "" {
				return []byte(typed)
			}
		case map[string]any:
			for _, nested := range []string{"content", "text", "result"} {
				if text, ok := typed[nested].(string); ok && text != "" {
					return []byte(text)
				}
			}
		}
	}
	return body
}

func truncateForError(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 256 {
		return text[:256]
	}
	return text
}

type zhipuTokenEntry struct {
	token  string
	expiry time.Time
}

var zhipuTokenCache sync.Map // api key -> zhipuTokenEntry

// zhipuJWT signs the same claim set as relay/channel/zhipu's getZhipuToken.
// It is duplicated rather than imported so the file relay does not pull the
// whole relay adaptor into its dependency graph; the claim shape is stable.
func zhipuJWT(apiKey string) string {
	if cached, ok := zhipuTokenCache.Load(apiKey); ok {
		if entry, ok := cached.(zhipuTokenEntry); ok && time.Now().Before(entry.expiry) {
			return entry.token
		}
	}
	id, secret, found := strings.Cut(apiKey, ".")
	if !found || id == "" || secret == "" {
		return ""
	}
	expiry := time.Now().Add(zhipuTokenTTLSeconds * time.Second)
	claims := jwt.MapClaims{
		"api_key":   id,
		"exp":       expiry.UnixNano() / 1e6,
		"timestamp": time.Now().UnixNano() / 1e6,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return ""
	}
	zhipuTokenCache.Store(apiKey, zhipuTokenEntry{token: signed, expiry: expiry})
	return signed
}
