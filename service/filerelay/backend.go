// Package filerelay forwards /v1/files calls to the provider that owns the
// bytes.
//
// The endpoint has one routing signal and it is not a model: an upload body
// carries a purpose, and every read path carries a gateway-issued file id. This
// package therefore stays outside the model-based distributor entirely — see
// SelectChannel for the purpose-only selection and Resolve for the id lookup.
package filerelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// Purposes understood by the gateway. Providers implement overlapping subsets,
// which is exactly why the purpose is a routing key: a channel declares the
// purposes it serves and never receives a request for another one.
const (
	PurposeFileExtract = "file-extract"
	PurposeBatch       = "batch"
	PurposeImage       = "image"
	PurposeVideo       = "video"
	PurposeVision      = "vision"
	PurposeAssistants  = "assistants"
	PurposeFineTune    = "fine-tune"
)

// KnownPurposes is the advisory list surfaced to clients and used to reject
// typos before a provider round trip. Validation still depends on what the
// selected channel declares.
var KnownPurposes = []string{
	PurposeFileExtract,
	PurposeBatch,
	PurposeImage,
	PurposeVideo,
	PurposeVision,
	PurposeAssistants,
	PurposeFineTune,
}

func IsKnownPurpose(purpose string) bool {
	for _, known := range KnownPurposes {
		if known == purpose {
			return true
		}
	}
	return false
}

// Object is the provider-side file description, normalised to the OpenAI shape.
type Object struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status,omitempty"`
}

// UploadInput streams the client's part straight through. The body is never
// buffered in memory: an upload can be hundreds of megabytes and the gateway is
// only a pipe.
type UploadInput struct {
	Filename    string
	Purpose     string
	ContentType string
	Size        int64
	// MaxBytes is the ceiling the selected channel declared. Zero falls back to
	// the backend's provider default.
	MaxBytes int64
	Body     io.Reader
}

// UploadResult carries either a retrievable provider object or, for providers
// whose parser answers with text instead of an id, the payload itself.
type UploadResult struct {
	Object *Object
	// Inline is non-nil when the provider returned the extraction directly. The
	// gateway then owns the content and the download handler never calls back.
	Inline      []byte
	ContentKind string
	MimeType    string
}

// Content is an open upstream response ready to be copied to the client.
type Content struct {
	Body        io.ReadCloser
	ContentType string
	// Length is the upstream Content-Length, or -1 when the provider streams
	// without one. It is forwarded verbatim so clients can show progress.
	Length int64
}

// UpstreamError keeps the provider's status code and error code so the handler
// can answer with the same class of failure instead of flattening everything
// to 500 or, worse, to a status the client reacts to incorrectly.
type UpstreamError struct {
	StatusCode int
	Message    string
	// Code is the provider's own error identifier. Moonshot answers an exhausted
	// balance with 429 exceeded_current_quota_error, which a client would retry
	// forever; the code is what distinguishes "wait" from "top up".
	Code string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned %d: %s", e.StatusCode, e.Message)
}

// LimitError reports an upload that exceeds the selected channel's ceiling. The
// handler turns it into a 413 rather than letting a provider reject it later.
type LimitError struct {
	Limit int64
	Size  int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("file is %d bytes, above the %d byte limit for this channel", e.Size, e.Limit)
}

// upstreamError prefers the provider's own message, which is usually the only
// thing that explains why an upload was refused.
func upstreamError(status int, body []byte) *UpstreamError {
	message := strings.TrimSpace(string(body))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	code := ""
	if err := common.Unmarshal(body, &envelope); err == nil {
		if envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		code = envelope.Error.Type
		if code == "" {
			code = envelope.Error.Code
		}
	}
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &UpstreamError{StatusCode: status, Message: message, Code: code}
}

// Backend is one provider's file protocol. List and Retrieve are deliberately
// absent: those are answered from the gateway's own records, because a provider
// cursor means nothing to another provider and a locally assembled list always
// returns ids that still resolve.
type Backend interface {
	Name() string
	Upload(ctx context.Context, ch *model.Channel, in *UploadInput) (*UploadResult, error)
	Delete(ctx context.Context, ch *model.Channel, upstreamID string) error
	Content(ctx context.Context, ch *model.Channel, upstreamID string) (*Content, error)
}

var ErrBackendUnsupported = errors.New("file relay is not implemented for this channel type")

// BackendForChannel picks the protocol by channel type. Custom channels are
// treated as OpenAI-compatible: that is what the type means, and the shape of
// /v1/files is the de facto convention among compatible gateways.
func BackendForChannel(ch *model.Channel) (Backend, error) {
	if ch == nil {
		return nil, ErrBackendUnsupported
	}
	switch ch.Type {
	case constant.ChannelTypeOpenAI,
		constant.ChannelTypeAzure,
		constant.ChannelTypeCustom:
		return openAIBackend{name: "openai"}, nil
	case constant.ChannelTypeMoonshot:
		return openAIBackend{name: "moonshot"}, nil
	case constant.ChannelTypeZhipu_v4:
		return zhipuBackend{}, nil
	default:
		return nil, fmt.Errorf("%w: channel type %d", ErrBackendUnsupported, ch.Type)
	}
}

// DefaultPurposes documents what each backend can serve, so the channel editor
// can suggest values instead of leaving an operator to guess.
func DefaultPurposes(ch *model.Channel) []string {
	if ch == nil {
		return nil
	}
	switch ch.Type {
	case constant.ChannelTypeOpenAI, constant.ChannelTypeAzure, constant.ChannelTypeCustom:
		return []string{PurposeBatch, PurposeVision, PurposeAssistants, PurposeFineTune}
	case constant.ChannelTypeMoonshot:
		return []string{PurposeFileExtract, PurposeImage, PurposeVideo, PurposeBatch}
	case constant.ChannelTypeZhipu_v4:
		return []string{PurposeFileExtract, PurposeBatch}
	default:
		return nil
	}
}

// joinBase appends an API path to a channel base URL without doubling a version
// segment the operator already typed into the field.
func joinBase(base string, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return path
	}
	for _, segment := range []string{"/api/paas/v4", "/v1"} {
		if strings.HasSuffix(base, segment) && strings.HasPrefix(path, segment+"/") {
			return base + strings.TrimPrefix(path, segment)
		}
	}
	return base + path
}

// resolveBase falls back to the provider's public endpoint when the channel
// leaves base_url empty, which is the common case for a first-party key.
func resolveBase(ch *model.Channel, fallback string) string {
	if base := strings.TrimSpace(ch.GetBaseURL()); base != "" {
		return base
	}
	return fallback
}

func readLimited(body io.Reader, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(body, limit))
}
