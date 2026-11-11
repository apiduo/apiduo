package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	// artifactRewriteMaxBody bounds the response buffered for rewriting. A
	// larger body is passed through untouched: buffering it would cost more
	// memory than the rewrite is worth.
	artifactRewriteMaxBody = 32 << 20
	// artifactRewriteMaxObject bounds one fetched artifact.
	artifactRewriteMaxObject = 64 << 20
	// artifactRewriteTimeout bounds one download-and-store round trip.
	artifactRewriteTimeout = 90 * time.Second
	// artifactRewriteMinBase64 is the length below which an inline value is
	// assumed to be an identifier, not media.
	artifactRewriteMinBase64 = 1024
)

// artifactFieldNames are the JSON keys that carry media references.
//
// The list is deliberately short and explicit. Guessing by value shape alone
// would rewrite unrelated links (docs, callbacks, pagination cursors) and turn
// a correct response into a broken one.
//
// "data" is included because every provider that inlines bytes uses it inside a
// media envelope: Claude (source.media_type + source.data) and Gemini
// (inlineData.mimeType + inlineData.data). The shape check in
// rewriteArtifactValue keeps a plain data container untouched.
//
// "b64_json" is deliberately absent: replacing an inline payload with a URL
// inside a field whose contract is "base64 string" breaks every client that
// decodes it. A channel that wants it can still select it explicitly.
var defaultArtifactFieldNames = map[string]struct{}{
	"url":        {},
	"image_url":  {},
	"audio_url":  {},
	"video_url":  {},
	"file_url":   {},
	"b64":        {},
	"base64":     {},
	"data":       {},
	"audio":      {},
	"audio_data": {},
}

// RewriteResponseArtifacts copies every media reference in a JSON response into
// object storage and replaces it with the stored address.
//
// It returns the new body and whether anything changed. Callers must send the
// original body when changed is false: this is an accelerator, never a reason
// to fail or alter a response the upstream already produced correctly.
func RewriteResponseArtifacts(ctx context.Context, channelID int, body []byte) ([]byte, bool) {
	if channelID <= 0 || len(body) == 0 || len(body) > artifactRewriteMaxBody {
		return body, false
	}
	// Check the chain before parsing anything. Without this, a channel that
	// never opted in still walks the body and downloads every referenced URL
	// before discovering it has nowhere to put the bytes - pointless traffic on
	// every response, and slow when a link is dead.
	chain := ResolveArtifactStorageChainForChannelID(channelID)
	if chain.Empty() {
		return body, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return body, false
	}

	var document any
	if err := common.Unmarshal(body, &document); err != nil {
		return body, false
	}

	rewriteCtx, cancel := context.WithTimeout(ctx, artifactRewriteTimeout)
	defer cancel()

	// Two mutually exclusive modes: the channel's selectors when it declared
	// any, otherwise the built-in field walk. Running both would let the walk
	// re-fetch a value a selector already replaced with a stored URL.
	var rewritten any
	var changed bool
	if len(chain.RewritePaths) > 0 {
		rewritten, changed = rewriteBySelectors(rewriteCtx, channelID, document, chain.RewritePaths)
	} else {
		fieldNames := chain.FieldNames
		if len(fieldNames) == 0 {
			fieldNames = defaultArtifactFieldNames
		}
		rewritten, changed = rewriteArtifactNode(rewriteCtx, channelID, document, fieldNames)
	}
	if !changed {
		return body, false
	}
	encoded, err := common.Marshal(rewritten)
	if err != nil {
		return body, false
	}
	// The re-encoded document can only be larger than the original; keep the
	// upper bound honest rather than emitting something unexpected.
	if len(encoded) > artifactRewriteMaxBody {
		return body, false
	}
	return encoded, true
}

func rewriteArtifactNode(ctx context.Context, channelID int, node any, fieldNames map[string]struct{}) (any, bool) {
	switch value := node.(type) {
	case map[string]any:
		changed := false
		for key, child := range value {
			if _, isArtifact := fieldNames[key]; isArtifact {
				if replacement, ok := rewriteArtifactValue(ctx, channelID, child); ok {
					value[key] = replacement
					changed = true
					continue
				}
			}
			if updated, ok := rewriteArtifactNode(ctx, channelID, child, fieldNames); ok {
				value[key] = updated
				changed = true
			}
		}
		return value, changed
	case []any:
		changed := false
		for index, child := range value {
			if updated, ok := rewriteArtifactNode(ctx, channelID, child, fieldNames); ok {
				value[index] = updated
				changed = true
			}
		}
		return value, changed
	default:
		return node, false
	}
}

// rewriteArtifactValue handles both shapes a media field takes: a bare string
// (data[].url, data[].b64_json) and an object (image_url: {url: "..."}).
func rewriteArtifactValue(ctx context.Context, channelID int, value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		return rewriteArtifactString(ctx, channelID, typed)
	case map[string]any:
		nested, ok := typed["url"].(string)
		if !ok {
			return value, false
		}
		replacement, replaced := rewriteArtifactString(ctx, channelID, nested)
		if !replaced {
			return value, false
		}
		typed["url"] = replacement
		return typed, true
	default:
		return value, false
	}
}

// rewriteArtifactString stores one reference and returns its new address.
func rewriteArtifactString(ctx context.Context, channelID int, raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw, false
	}

	switch {
	case strings.HasPrefix(trimmed, "data:"):
		return storeInlineData(ctx, channelID, trimmed)
	case strings.HasPrefix(trimmed, "http://"), strings.HasPrefix(trimmed, "https://"):
		return storeRemoteArtifact(ctx, channelID, trimmed)
	case looksLikeInlineBase64(trimmed):
		return storeBase64Artifact(ctx, channelID, trimmed)
	default:
		return raw, false
	}
}

// storeInlineData handles a data: URL, which carries the bytes themselves.
func storeInlineData(ctx context.Context, channelID int, raw string) (any, bool) {
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return raw, false
	}
	header := raw[len("data:"):comma]
	if !strings.Contains(header, "base64") {
		// Percent-encoded payloads are rare and cheap to skip; the upstream
		// link still works.
		return raw, false
	}
	contentType := strings.TrimSpace(strings.Split(header, ";")[0])
	data, err := base64.StdEncoding.DecodeString(raw[comma+1:])
	if err != nil || len(data) == 0 || len(data) > artifactRewriteMaxObject {
		return raw, false
	}
	return persistAndDescribe(ctx, channelID, data, contentType, raw)
}

// storeBase64Artifact handles a bare base64 payload (b64_json).
func storeBase64Artifact(ctx context.Context, channelID int, raw string) (any, bool) {
	data, err := decodeLooseBase64(raw)
	if err != nil || len(data) == 0 || len(data) > artifactRewriteMaxObject {
		return raw, false
	}
	return persistAndDescribe(ctx, channelID, data, sniffContentType(data), raw)
}

// storeRemoteArtifact downloads a referenced object, stores it, and hands back
// the stored address.
func storeRemoteArtifact(ctx context.Context, channelID int, raw string) (any, bool) {
	if _, err := artifactFetchURLValidator(raw); err != nil {
		// A private or malformed address is left untouched: refusing to fetch it
		// is the point, and the upstream link keeps working for the client.
		return raw, false
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return raw, false
	}
	request.Header.Set("User-Agent", "apiduo-artifact-fetcher")
	response, err := artifactFetchClient.Do(request)
	if err != nil {
		return raw, false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return raw, false
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, artifactRewriteMaxObject+1))
	if err != nil || len(data) == 0 || len(data) > artifactRewriteMaxObject {
		return raw, false
	}
	return persistAndDescribe(ctx, channelID, data, contentTypeFor(response.Header.Get("Content-Type"), data), raw)
}

// FetchArtifactBytes downloads one remote reference and hands back the bytes.
//
// The response rewriter stores what it fetches; a caller that must honour
// `response_format: b64_json` needs the bytes instead. Both go through the same
// validator and client so the SSRF rules, the size ceiling and the timeout are
// identical - a second download path would be a second thing to get wrong.
func FetchArtifactBytes(ctx context.Context, raw string) ([]byte, string, error) {
	if _, err := artifactFetchURLValidator(raw); err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", "apiduo-artifact-fetcher")
	response, err := artifactFetchClient.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch reference: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, artifactRewriteMaxObject+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", errors.New("reference body is empty")
	}
	if len(data) > artifactRewriteMaxObject {
		return nil, "", errors.New("reference exceeds the artifact size ceiling")
	}
	return data, contentTypeFor(response.Header.Get("Content-Type"), data), nil
}

// persistAndDescribe stores the bytes and returns the address to write back.
func persistAndDescribe(ctx context.Context, channelID int, data []byte, contentType, original string) (any, bool) {
	stored, err := PersistArtifactBytes(ctx, channelID, artifactObjectKey(data, contentType), contentType, data)
	if err != nil || stored == nil || stored.URL == "" {
		if err != nil && !errors.Is(err, ErrTaskArtifactStoreDisabled) {
			common.SysError(common.LogText("artifact response rewrite failed to store object: %v", err))
		}
		return original, false
	}
	return stored.URL, true
}

// --- custom selectors -------------------------------------------------------

// pathSegment is one step of a selector.
type pathSegment struct {
	kind  segmentKind
	key   string
	index int
}

// artifactSelector is one configured rule: where to look, and what to do with
// the result.
type artifactSelector struct {
	segments []pathSegment
	// targetField, when set, receives the stored address instead of the original
	// field. That is what makes an inline payload field safe to handle: the
	// contract of b64_json stays "base64 string" only if we do not overwrite it
	// with a URL.
	targetField string
	// clearSource blanks the original field after the address is written
	// elsewhere, so the response does not carry both a payload and a link.
	clearSource bool
}

type segmentKind int

const (
	segmentKey segmentKind = iota
	segmentIndex
	segmentWildcard
)

// parseArtifactPath accepts the JSONPath subset this feature needs. Both the
// bracket and the dot form of an index work, and "*" matches every child:
//
//	$.data[*].url
//	$.data.0.url
//	$.candidates[*].content.parts[*].inlineData.data
func parseArtifactPath(raw string) ([]pathSegment, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, errors.New("empty selector")
	}
	text = strings.TrimPrefix(text, "$")
	// Normalise brackets into dots so a single split handles both notations.
	text = strings.ReplaceAll(text, "[", ".")
	text = strings.ReplaceAll(text, "]", "")
	text = strings.TrimPrefix(text, ".")
	if text == "" {
		return nil, errors.New("selector must address a field, not the document root")
	}

	parts := strings.Split(text, ".")
	segments := make([]pathSegment, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
			return nil, fmt.Errorf("selector %q has an empty step", raw)
		case part == "*":
			segments = append(segments, pathSegment{kind: segmentWildcard})
		case isAllDigits(part):
			index, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("selector %q has an invalid index: %w", raw, err)
			}
			segments = append(segments, pathSegment{kind: segmentIndex, index: index})
		default:
			segments = append(segments, pathSegment{kind: segmentKey, key: part})
		}
	}
	return segments, nil
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// parseArtifactSelector splits "path => field [clear]" into its parts.
//
// The arrow form exists for fields whose contract is a payload rather than a
// link: writing a URL into b64_json would break every client that decodes it, so
// the address goes to a sibling field instead.
func parseArtifactSelector(raw string) (artifactSelector, error) {
	selector := artifactSelector{}
	path := raw
	if index := strings.Index(raw, "=>"); index >= 0 {
		path = strings.TrimSpace(raw[:index])
		rest := strings.Fields(raw[index+2:])
		if len(rest) == 0 {
			return artifactSelector{}, fmt.Errorf("selector %q is missing a target field after =>", raw)
		}
		selector.targetField = rest[0]
		if len(rest) > 1 && strings.EqualFold(rest[1], "clear") {
			selector.clearSource = true
		}
	}
	segments, err := parseArtifactPath(path)
	if err != nil {
		return artifactSelector{}, err
	}
	selector.segments = segments
	return selector, nil
}

// pathTarget is a writable location inside the decoded document.
type pathTarget struct {
	object  map[string]any
	field   string
	array   []any
	index   int
	inArray bool
}

func (t pathTarget) value() any {
	if t.inArray {
		return t.array[t.index]
	}
	return t.object[t.field]
}

func (t pathTarget) assign(value any) {
	if t.inArray {
		t.array[t.index] = value
		return
	}
	t.object[t.field] = value
}

// assignSibling writes into the object that contains the matched value. It only
// works when that container is an object; a bare array element has no sibling.
func (t pathTarget) assignSibling(field string, value any) bool {
	if t.inArray || t.object == nil {
		return false
	}
	t.object[field] = value
	return true
}

// collectPathTargets walks the document and records every location the selector
// addresses. It records the parent plus the key so the caller can assign a
// replacement, which a plain value would not allow.
func collectPathTargets(node any, segments []pathSegment, targets *[]pathTarget) {
	if len(segments) == 0 {
		return
	}
	segment := segments[0]
	remaining := segments[1:]
	last := len(remaining) == 0

	switch container := node.(type) {
	case map[string]any:
		switch segment.kind {
		case segmentIndex:
			// An index cannot address an object.
			return
		case segmentWildcard:
			for key, child := range container {
				if last {
					*targets = append(*targets, pathTarget{object: container, field: key})
					continue
				}
				collectPathTargets(child, remaining, targets)
			}
		default:
			child, ok := container[segment.key]
			if !ok {
				return
			}
			if last {
				*targets = append(*targets, pathTarget{object: container, field: segment.key})
				return
			}
			collectPathTargets(child, remaining, targets)
		}
	case []any:
		switch segment.kind {
		case segmentKey:
			// A key cannot address an array.
			return
		case segmentWildcard:
			for index, child := range container {
				if last {
					*targets = append(*targets, pathTarget{array: container, index: index, inArray: true})
					continue
				}
				collectPathTargets(child, remaining, targets)
			}
		default:
			if segment.index < 0 || segment.index >= len(container) {
				return
			}
			if last {
				*targets = append(*targets, pathTarget{array: container, index: segment.index, inArray: true})
				return
			}
			collectPathTargets(container[segment.index], remaining, targets)
		}
	}
}

// rewriteBySelectors applies every configured selector.
func rewriteBySelectors(ctx context.Context, channelID int, document any, selectors []string) (any, bool) {
	changed := false
	for _, raw := range selectors {
		selector, err := parseArtifactSelector(raw)
		if err != nil {
			// A typo costs one selector, never the response.
			common.SysError(common.LogText("artifact rewrite selector ignored: %v", err))
			continue
		}
		targets := make([]pathTarget, 0, 4)
		collectPathTargets(document, selector.segments, &targets)
		for _, target := range targets {
			replacement, ok := rewriteArtifactValue(ctx, channelID, target.value())
			if !ok {
				continue
			}
			if selector.targetField == "" {
				target.assign(replacement)
				changed = true
				continue
			}
			if !target.assignSibling(selector.targetField, replacement) {
				// The matched value is not inside an object, so there is nowhere
				// to put the address; leave the response untouched.
				continue
			}
			if selector.clearSource {
				target.assign(nil)
			}
			changed = true
		}
	}
	return document, changed
}

// artifactFetchClient is a separate client from the storage one: it fetches from
// arbitrary upstream CDNs and must never reuse the signed-request transport.
var artifactFetchClient = &http.Client{
	Timeout: artifactRewriteTimeout,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
	},
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		// Every hop is validated: a public URL must not bounce into the private
		// network, which is the classic way around an initial check.
		_, err := artifactFetchURLValidator(request.URL.String())
		return err
	},
}

// artifactFetchURLValidator rejects anything the gateway must not reach. The
// upstream controls this value, so an unvalidated fetch is a request forgery
// primitive pointed at the gateway's own network.
//
// It is a variable so tests can point the fetcher at a loopback server; nothing
// in production replaces it.
var artifactFetchURLValidator = validateArtifactFetchURL

func validateArtifactFetchURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, errors.New("missing host")
	}

	addresses, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("host did not resolve")
	}
	for _, address := range addresses {
		ip := address.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("refusing to fetch private address %s", ip)
		}
	}
	return parsed, nil
}

// artifactObjectKey derives the key from the content, so the same image served
// twice lands on one object instead of accumulating duplicates.
func artifactObjectKey(data []byte, contentType string) string {
	digest := sha256.Sum256(data)
	return "response/" + time.Now().Format("2006/01/02") + "/" +
		hex.EncodeToString(digest[:16]) + artifactExtension(contentType, data)
}

var artifactExtensionByType = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/webp":    ".webp",
	"image/gif":     ".gif",
	"image/svg+xml": ".svg",
	"video/mp4":     ".mp4",
	"video/webm":    ".webm",
	"audio/mpeg":    ".mp3",
	"audio/wav":     ".wav",
}

func artifactExtension(contentType string, data []byte) string {
	base := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if extension, ok := artifactExtensionByType[strings.ToLower(base)]; ok {
		return extension
	}
	return artifactExtensionByType[sniffContentType(data)]
}

// sniffContentType identifies the common media formats from their magic bytes.
// Upstreams frequently serve objects as application/octet-stream, and the
// extension is what makes a rewritten link useful in a browser.
// contentTypeFor 决定存进对象存储的类型。
//
// 上游偶尔把自己手里的字节一律标成 application/octet-stream（COS 直链就常见），
// 照抄下去会让图片在严格客户端里变成"下载一个文件"而不是"显示一张图" ——
// 而对象 key 那边已经按魔数补了 .png，两边就自相矛盾了。这种情况下按字节重认一次；
// 上游明确声明了具体类型就以它为准。
func contentTypeFor(header string, data []byte) string {
	value := strings.TrimSpace(header)
	if value == "" || isGenericBinaryType(value) {
		return sniffContentType(data)
	}
	return value
}

func isGenericBinaryType(value string) bool {
	base, _, _ := strings.Cut(value, ";")
	switch strings.TrimSpace(strings.ToLower(base)) {
	case "application/octet-stream", "binary/octet-stream", "application/binary":
		return true
	}
	return false
}

func sniffContentType(data []byte) string {
	if len(data) < 12 {
		return "application/octet-stream"
	}
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case bytes.HasPrefix(data, []byte("RIFF")) && bytes.HasPrefix(data[8:], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(data, []byte("\x00\x00\x00")) && bytes.HasPrefix(data[4:], []byte("ftyp")):
		return "video/mp4"
	case bytes.HasPrefix(data, []byte("ID3")):
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

// looksLikeInlineBase64 is a cheap pre-filter: short values are identifiers, and
// a value containing characters outside the alphabet cannot be decoded anyway.
func looksLikeInlineBase64(value string) bool {
	if len(value) < artifactRewriteMinBase64 {
		return false
	}
	if strings.ContainsAny(value, " \n\t\r") {
		return false
	}
	// Sample the head instead of scanning megabytes twice.
	head := value
	if len(head) > 256 {
		head = head[:256]
	}
	for _, character := range head {
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '+', character == '/', character == '=', character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

// RewriteResponseArtifactsInPlace substitutes the response body with a
// rewritten one when, and only when, the rewrite succeeded.
//
// It is total: every failure path returns the response untouched, because the
// upstream already produced a valid answer and storage is a side effect. The
// caller never has to handle an error.
func RewriteResponseArtifactsInPlace(ctx context.Context, channelID int, response *http.Response) *http.Response {
	if response == nil || response.Body == nil || channelID <= 0 {
		return response
	}
	// Resolved before reading the body: an unconfigured channel must cost
	// nothing, not "read 32 MB, then give up".
	if ResolveArtifactStorageChainForChannelID(channelID).Empty() {
		return response
	}
	// A stream is unbounded and incremental; buffering it would defeat its
	// purpose and the rewrite could never be applied anyway.
	contentType := response.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return response
	}
	// Chunked responses report -1 and are still readable, so only an explicitly
	// oversized body is skipped.
	if response.ContentLength > artifactRewriteMaxBody {
		return response
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, artifactRewriteMaxBody+1))
	if err != nil {
		// The bytes already consumed cannot be fetched again; surface them ahead
		// of whatever is left so the client still receives a complete body.
		response.Body = &artifactReplayBody{
			Reader: io.MultiReader(bytes.NewReader(body), response.Body),
			closer: response.Body,
		}
		return response
	}
	if len(body) > artifactRewriteMaxBody {
		response.Body = &artifactReplayBody{
			Reader: io.MultiReader(bytes.NewReader(body), response.Body),
			closer: response.Body,
		}
		return response
	}

	originalBody := response.Body
	rewritten, changed := RewriteResponseArtifacts(ctx, channelID, body)
	if !changed {
		response.Body = &artifactReplayBody{Reader: bytes.NewReader(body), closer: originalBody}
		return response
	}
	originalBody.Close()
	response.Body = io.NopCloser(bytes.NewReader(rewritten))
	response.ContentLength = int64(len(rewritten))
	if response.Header != nil {
		response.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
		// The body changed, so any integrity header computed for the original
		// is now wrong and would make a strict client reject the response.
		response.Header.Del("Content-MD5")
		response.Header.Del("ETag")
	}
	return response
}

// artifactReplayBody keeps the original closer while serving replaced bytes.
type artifactReplayBody struct {
	io.Reader
	closer io.Closer
}

func (b *artifactReplayBody) Close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

func decodeLooseBase64(value string) ([]byte, error) {
	if data, err := base64.StdEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	if data, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	if data, err := base64.URLEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}
