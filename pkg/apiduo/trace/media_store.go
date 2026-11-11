package trace

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Media read path for the trace drawer.
//
// Images live in Langfuse's media bucket; the payload only carries a URL to
// this gateway. Rendering means: resolve the media id through Langfuse,
// follow its presigned MinIO link, and hand the bytes back. That is two
// internal round trips plus a decode, so results are cached — the same
// trace is opened repeatedly during an investigation, and a 1.3 MB PNG
// re-fetched on every open is exactly the latency this feature exists to
// remove.

// ErrMediaNotFound means Langfuse has no confirmed media row for the id.
// An unconfirmed upload answers 404, which is why the exporter only mints a
// URL after the confirm step succeeded.
var ErrMediaNotFound = errors.New("media not found")

const (
	// A multi-image payload at 8 MB each already exceeds this; one entry
	// bigger than the whole budget would evict everything else on arrival.
	mediaCacheMaxBytes = 64 << 20
	mediaCacheMaxEntry = 12 << 20
	// A 40-megapixel source is not a screenshot; refuse to decode it rather
	// than let one pathological file allocate a gigabyte.
	mediaMaxPixels = 40_000_000
	mediaMaxWidth  = 2048
)

var mediaIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

// Suffixes the write path puts on a media URL. The extension carries no
// meaning — Langfuse names the object itself — it only makes the URL readable
// and gives the drawer a hint before the bytes arrive.
//
// This is a closed list on purpose. Parsing used to reverse the write-path
// mapping by asking mediaExtForMime("image/"+ext), which fails for the one
// extension that mapping itself produces: mediaExtForMime("image/jpeg") is
// "jpg", but feeding "jpg" back as "image/jpg" matches no case, so the suffix
// survived into the id, mediaIDPattern rejected the dot, and every JPEG
// response 404'd. Only PNG had ever been exercised end to end.
var mediaURLSuffixes = map[string]struct{}{
	"png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "webp": {},
}

// ParseMediaParam splits "<id>.<ext>" as it appears in the URL path.
func ParseMediaParam(param string) (string, bool) {
	id := param
	if dot := strings.LastIndexByte(id, '.'); dot > 0 {
		ext := strings.ToLower(id[dot+1:])
		if _, ok := mediaURLSuffixes[ext]; ok {
			id = id[:dot]
		}
	}
	if !mediaIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// LoadMedia returns the bytes for one media id, downscaled to width when the
// source is larger. width <= 0 means the original.
func LoadMedia(id string, width int) ([]byte, string, error) {
	if !mediaIDPattern.MatchString(id) {
		return nil, "", ErrMediaNotFound
	}
	width = clampWidth(width)
	key := id
	if width > 0 {
		key = id + "|w" + strconv.Itoa(width)
	}
	if data, mime, ok := mediaCacheGet(key); ok {
		return data, mime, nil
	}

	raw, mime, err := fetchMedia(id)
	if err != nil {
		return nil, "", err
	}
	mediaCachePut(id, raw, mime)

	if width <= 0 {
		return raw, mime, nil
	}
	thumb, thumbMime := thumbnail(raw, mime, width)
	if len(thumb) >= len(raw) {
		// Re-encoding made it bigger (a tiny PNG, or a source already at the
		// requested size). The original is the better answer.
		return raw, mime, nil
	}
	mediaCachePut(key, thumb, thumbMime)
	return thumb, thumbMime, nil
}

func clampWidth(width int) int {
	if width <= 0 {
		return 0
	}
	if width < 32 {
		return 32
	}
	if width > mediaMaxWidth {
		return mediaMaxWidth
	}
	return width
}

// fetchMedia resolves one id through Langfuse and streams the object out of
// MinIO. The second hop is a presigned URL, so it must not carry our Basic
// auth: S3 rejects a request whose headers were not part of the signature.
func fetchMedia(id string) ([]byte, string, error) {
	conf := current()
	if conf == nil || conf.Host == "" || conf.PublicKey == "" {
		return nil, "", ErrTraceDisabled
	}
	client := &http.Client{Timeout: fetchTimeout}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(conf.PublicKey+":"+conf.SecretKey))

	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(conf.Host, "/")+"/api/public/media/"+id, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrMediaNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("langfuse media HTTP %d", resp.StatusCode)
	}

	var ref struct {
		ContentType string `json:"contentType"`
		URL         string `json:"url"`
	}
	if err := json.Unmarshal(body, &ref); err != nil || ref.URL == "" {
		return nil, "", fmt.Errorf("langfuse media response has no url")
	}

	obj, err := client.Get(ref.URL)
	if err != nil {
		return nil, "", err
	}
	defer obj.Body.Close()
	if obj.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media object HTTP %d", obj.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, mediaCacheMaxEntry))
	if err != nil {
		return nil, "", err
	}
	mime := ref.ContentType
	if mime == "" {
		mime = obj.Header.Get("Content-Type")
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return data, mime, nil
}

// thumbnail re-encodes to JPEG at the requested width. JPEG rather than WebP
// because the standard library has no WebP encoder and pulling in cgo for a
// drawer tile is not a trade worth making.
func thumbnail(raw []byte, mime string, width int) ([]byte, string) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return raw, mime
	}
	if cfg.Width*cfg.Height > mediaMaxPixels {
		return raw, mime
	}
	if cfg.Width <= width {
		return raw, mime
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw, mime
	}
	bounds := img.Bounds()
	height := bounds.Dy() * width / bounds.Dx()
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return raw, mime
	}
	return buf.Bytes(), "image/jpeg"
}

// ── LRU ──────────────────────────────────────────────────────────────────
//
// Deliberately tiny: the working set is "the trace someone is looking at
// right now", not a cache worth tuning.

type mediaCacheEntry struct {
	key  string
	data []byte
	mime string
}

var mediaCache = struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
	bytes int
}{
	order: list.New(),
	items: make(map[string]*list.Element),
}

func mediaCacheGet(key string) ([]byte, string, bool) {
	mediaCache.mu.Lock()
	defer mediaCache.mu.Unlock()
	el, ok := mediaCache.items[key]
	if !ok {
		return nil, "", false
	}
	mediaCache.order.MoveToFront(el)
	entry := el.Value.(*mediaCacheEntry)
	return entry.data, entry.mime, true
}

func mediaCachePut(key string, data []byte, mime string) {
	if len(data) == 0 || len(data) > mediaCacheMaxEntry {
		return
	}
	mediaCache.mu.Lock()
	defer mediaCache.mu.Unlock()
	if el, ok := mediaCache.items[key]; ok {
		mediaCache.order.MoveToFront(el)
		entry := el.Value.(*mediaCacheEntry)
		mediaCache.bytes += len(data) - len(entry.data)
		entry.data, entry.mime = data, mime
	} else {
		el := mediaCache.order.PushFront(&mediaCacheEntry{key: key, data: data, mime: mime})
		mediaCache.items[key] = el
		mediaCache.bytes += len(data)
	}
	for mediaCache.bytes > mediaCacheMaxBytes {
		back := mediaCache.order.Back()
		if back == nil {
			break
		}
		entry := back.Value.(*mediaCacheEntry)
		mediaCache.order.Remove(back)
		delete(mediaCache.items, entry.key)
		mediaCache.bytes -= len(entry.data)
	}
}

// MediaCacheReset is for tests.
func MediaCacheReset() {
	mediaCache.mu.Lock()
	defer mediaCache.mu.Unlock()
	mediaCache.order.Init()
	mediaCache.items = make(map[string]*list.Element)
	mediaCache.bytes = 0
}
