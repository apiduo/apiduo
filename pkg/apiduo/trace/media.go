package trace

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Image externalization.
//
// An image response is a 1.7 MB base64 line. Left inside the span it is paid
// for three times: Langfuse blob storage, ClickHouse read amplification, and
// the browser that has to lay the string out. The bytes are not the problem —
// the *encoding inside a text field* is. So the flush worker lifts them out,
// hands them to Langfuse's media store and leaves one short URL behind.
//
// Why Langfuse's media store rather than our own: Langfuse already owns a
// bucket for trace blobs and already knows how to read from it. A second copy
// would double the bytes for no benefit. The URL we leave in the payload
// points at *our* gateway though — Langfuse's own media URL is a presigned
// MinIO link (`http://langfuse-minio:9000/...`), which no browser can reach
// and which an https page would block as mixed content anyway.
//
// Contract with the rest of the package: this runs in the flush worker only
// (BuildExportBody), never on the request path. Every step is bounded, and
// any failure falls back to the original base64 — a bigger trace is an
// acceptable outcome, a lost one is not.

const (
	// Below this a base64 run is a thumbnail or a tracking pixel: the upload
	// round trip costs more than it saves.
	mediaMinB64Chars = 2048
	// A decoded image larger than this is not a preview, it is a download.
	// Leave it in place rather than push 20 MB through the exporter.
	mediaMaxRawBytes = 8 << 20
	// Per span. An image batch of four plus a mask is already unusual; a
	// hundred is a payload shape we do not want to amplify into a hundred
	// uploads inside one flush.
	mediaMaxPerSpan = 3
	// Per flush. Bounds the worker's dwell time on a batch of image traffic.
	mediaMaxPerBatch = 24
	// Per HTTP request (three per image: create, PUT, confirm).
	mediaTimeout = 2 * time.Second
	// Whole-batch budget. When it expires every remaining image keeps its
	// base64 so the batch still ships on time.
	mediaBudget = 5 * time.Second
	// After this many consecutive failures we stop trying for a while: a
	// Langfuse that cannot serve media must not turn every flush into a
	// five-second penalty on the queue behind it.
	mediaMaxConsecutiveFails = 3
	mediaFailCooldown        = 5 * time.Minute
)

var (
	// `data:image/png;base64,…` anywhere in the body.
	mediaDataURI = regexp.MustCompile(`data:image/(png|jpeg|jpg|webp|gif);base64,([A-Za-z0-9+/=]{512,})`)
	// A JSON string value that is entirely base64. Covers OpenAI `b64_json`,
	// Anthropic `source.data` and Gemini `inlineData.data` without teaching
	// this file three vendor shapes — the decoder plus the magic-byte check
	// rejects anything that is not a picture.
	// Go's regexp caps repetition at 1000, so the lower bound here is 1000 and
	// decodeB64 enforces the real 2048 threshold.
	mediaJSONB64 = regexp.MustCompile(`"([A-Za-z0-9+/=]{1000,})"`)
)

var (
	mediaConsecutiveFails atomic.Int32
	mediaDisabledUntil    atomic.Int64
	mediaLastWarn         atomic.Int64
)

type mediaUploader struct {
	endpoint string
	auth     string
	baseURL  string
	client   *http.Client
	deadline time.Time
	batch    int
}

// newMediaUploader returns nil when the feature is off. A nil uploader is the
// fast path: rewrite does one nil check and returns the original string.
func newMediaUploader(conf *Config) *mediaUploader {
	if conf == nil || conf.MediaBaseURL == "" || conf.Host == "" || conf.PublicKey == "" {
		return nil
	}
	if time.Now().Unix() < mediaDisabledUntil.Load() {
		return nil
	}
	return &mediaUploader{
		endpoint: strings.TrimSuffix(conf.Host, "/"),
		auth: "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(conf.PublicKey+":"+conf.SecretKey)),
		baseURL:  strings.TrimSuffix(conf.MediaBaseURL, "/"),
		client:   &http.Client{Timeout: mediaTimeout},
		deadline: time.Now().Add(mediaBudget),
	}
}

// rewrite replaces every image it can lift out of text with a public URL. A
// no-op unless the payload is large enough to possibly hold one.
func (u *mediaUploader) rewrite(text, traceID, field string) string {
	if u == nil || len(text) < mediaMinB64Chars {
		return text
	}
	span := 0

	out := mediaDataURI.ReplaceAllStringFunc(text, func(match string) string {
		if span >= mediaMaxPerSpan || u.exhausted() {
			return match
		}
		sub := mediaDataURI.FindStringSubmatch(match)
		raw, ok := decodeB64(sub[2])
		if !ok || len(raw) > mediaMaxRawBytes {
			return match
		}
		mime, ext, ok := sniffImage(raw)
		if !ok {
			return match
		}
		url, ok := u.upload(raw, mime, ext, traceID, field)
		if !ok {
			return match
		}
		span++
		return url
	})

	if span >= mediaMaxPerSpan || u.exhausted() {
		return out
	}
	return mediaJSONB64.ReplaceAllStringFunc(out, func(match string) string {
		if span >= mediaMaxPerSpan || u.exhausted() {
			return match
		}
		sub := mediaJSONB64.FindStringSubmatch(match)
		raw, ok := decodeB64(sub[1])
		if !ok || len(raw) > mediaMaxRawBytes {
			return match
		}
		mime, ext, ok := sniffImage(raw)
		if !ok {
			return match
		}
		url, ok := u.upload(raw, mime, ext, traceID, field)
		if !ok {
			return match
		}
		span++
		return `"` + url + `"`
	})
}

func (u *mediaUploader) exhausted() bool {
	return u.batch >= mediaMaxPerBatch || time.Now().After(u.deadline)
}

// upload runs Langfuse's three-step media handshake and returns the URL the
// drawer and the Langfuse UI should load the picture from.
//
// The order matters. The presigned PUT only succeeds when it carries the
// checksum header S3 signed into the URL, and the object stays invisible to
// reads until the PATCH confirms the upload — an unconfirmed media row
// answers 404 no matter how many times you ask.
func (u *mediaUploader) upload(raw []byte, mime, ext, traceID, field string) (string, bool) {
	if len(traceID) != 32 {
		return "", false
	}
	sum := sha256.Sum256(raw)
	sumB64 := base64.StdEncoding.EncodeToString(sum[:])

	create, _ := json.Marshal(map[string]any{
		"contentType":   mime,
		"contentLength": len(raw),
		"sha256Hash":    sumB64,
		"field":         field,
		"traceId":       traceID,
	})
	body, err := u.do(http.MethodPost, u.endpoint+"/api/public/media", create)
	if err != nil {
		u.fail("create", err)
		return "", false
	}
	var created struct {
		MediaID   string `json:"mediaId"`
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.MediaID == "" || created.UploadURL == "" {
		u.fail("create", fmt.Errorf("bad response: %s", clipRunes(string(body), 120)))
		return "", false
	}

	req, err := http.NewRequest(http.MethodPut, created.UploadURL, bytes.NewReader(raw))
	if err != nil {
		u.fail("sign", err)
		return "", false
	}
	// SignedHeaders is content-length;content-type;host;x-amz-checksum-sha256.
	// Content-Length comes from bytes.Reader, the other two are mandatory.
	req.Header.Set("Content-Type", mime)
	req.Header.Set("x-amz-checksum-sha256", sumB64)
	if _, err := u.send(req); err != nil {
		u.fail("put", err)
		return "", false
	}

	confirm, _ := json.Marshal(map[string]any{
		"uploadedAt":       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"uploadHttpStatus": http.StatusOK,
	})
	if _, err := u.do(http.MethodPatch, u.endpoint+"/api/public/media/"+created.MediaID, confirm); err != nil {
		u.fail("confirm", err)
		return "", false
	}

	mediaConsecutiveFails.Store(0)
	u.batch++
	return u.baseURL + "/api/apiduo/trace/media/" + created.MediaID + "." + ext, true
}

func (u *mediaUploader) do(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return u.send(req)
}

func (u *mediaUploader) send(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", u.auth)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, clipRunes(string(body), 160))
	}
	return body, nil
}

func (u *mediaUploader) fail(stage string, err error) {
	n := mediaConsecutiveFails.Add(1)
	if n >= mediaMaxConsecutiveFails {
		mediaDisabledUntil.Store(time.Now().Add(mediaFailCooldown).Unix())
		mediaConsecutiveFails.Store(0)
	}
	now := time.Now().Unix()
	last := mediaLastWarn.Load()
	if now-last > 60 && mediaLastWarn.CompareAndSwap(last, now) {
		log.Printf("[apiduo-trace] media externalize %s failed (%d in a row), keeping base64: %v", stage, n, err)
	}
}

// decodeB64 tolerates line breaks and missing padding, both of which show up
// in real payloads (Node's Buffer.toString wraps at 76 chars).
func decodeB64(s string) ([]byte, bool) {
	if len(s) < mediaMinB64Chars {
		return nil, false
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ' ' || r == '\t'
	}) >= 0 {
		s = strings.Map(func(r rune) rune {
			switch r {
			case '\n', '\r', ' ', '\t':
				return -1
			}
			return r
		}, s)
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, true
	}
	if raw, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return raw, true
	}
	return nil, false
}

// sniffImage decides by magic bytes, not by the declared type: upstreams
// mislabel PNGs as JPEGs, and a wrong mime makes the media row unreadable.
func sniffImage(raw []byte) (mime, ext string, ok bool) {
	switch {
	case len(raw) > 8 && bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", "png", true
	case len(raw) > 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "image/jpeg", "jpg", true
	case len(raw) > 6 && (bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a"))):
		return "image/gif", "gif", true
	case len(raw) > 12 && bytes.HasPrefix(raw, []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "image/webp", "webp", true
	}
	return "", "", false
}

// mediaExtForMime maps a response Content-Type back to an extension.
func mediaExtForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return ""
}
