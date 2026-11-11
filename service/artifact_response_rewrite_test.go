package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rewriteFakeS3 accepts object writes and remembers them.
type rewriteFakeS3 struct {
	mu         sync.Mutex
	objects    map[string][]byte
	failStatus int
}

func (f *rewriteFakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	_, key, found := strings.Cut(path, "/")
	if !found || r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.objects[key] = body
	w.Header().Set("ETag", `"etag"`)
	w.WriteHeader(http.StatusOK)
}

func (f *rewriteFakeS3) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects)
}

func setupRewriteTestDB(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rewrite.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))

	previousDB := model.DB
	previousCache := common.MemoryCacheEnabled
	model.DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCache
	})
}

func resetArtifactTargetHealth(t *testing.T) {
	t.Helper()
	artifactTargetHealth.mu.Lock()
	clear(artifactTargetHealth.failures)
	artifactTargetHealth.mu.Unlock()
	t.Cleanup(func() {
		artifactTargetHealth.mu.Lock()
		clear(artifactTargetHealth.failures)
		artifactTargetHealth.mu.Unlock()
	})
}

func withGlobalArtifactStorage(t *testing.T, mutate func(*system_setting.ArtifactStorageSetting)) {
	t.Helper()
	global := system_setting.GetArtifactStorageSetting()
	saved := *global
	t.Cleanup(func() { *global = saved })
	mutate(global)
}

// allowLoopbackFetches disables the private-address guard for tests that need to
// reach an httptest server. Tests asserting the guard must not call it.
func allowLoopbackFetches(t *testing.T) {
	t.Helper()
	previous := artifactFetchURLValidator
	artifactFetchURLValidator = func(raw string) (*url.URL, error) { return url.Parse(raw) }
	t.Cleanup(func() { artifactFetchURLValidator = previous })
}

func createRewriteTestChannel(t *testing.T, settings string) int {
	t.Helper()
	channel := &model.Channel{Name: "rewrite-test", Key: "k", Status: 1, OtherSettings: settings}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel.Id
}

func pointStorageAt(t *testing.T, server string, publicBase string) {
	t.Helper()
	withGlobalArtifactStorage(t, func(setting *system_setting.ArtifactStorageSetting) {
		setting.Enabled = true
		pathStyle := true
		setting.Targets = []system_setting.ArtifactStorageTarget{{
			Provider:      system_setting.ArtifactStorageProviderMinIO,
			Endpoint:      server,
			Bucket:        "artifacts",
			Region:        "us-east-1",
			AccessKey:     "key",
			SecretKey:     "secret",
			PathStyle:     &pathStyle,
			PublicBaseURL: publicBase,
		}}
	})
}

// pngBytes is large enough that its base64 form clears the "looks like an
// identifier" threshold; a real generated image is always well past it.
func pngBytes() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x42}, 4096)...)
}

func TestRewriteStoresRemoteImageAndRewritesURL(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)
	allowLoopbackFetches(t)

	imageBytes := pngBytes()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	t.Cleanup(cdn.Close)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	body := []byte(`{"created":1,"data":[{"url":"` + cdn.URL + `/image.png"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed, "a remote image must be replaced with its stored address")

	text := string(rewritten)
	assert.NotContains(t, text, cdn.URL, "the upstream link must not survive")
	assert.Contains(t, text, "https://cdn.example.com/response/")
	assert.Contains(t, text, ".png")
	assert.Equal(t, 1, storage.count())
	for _, stored := range storage.objects {
		assert.Equal(t, imageBytes, stored, "the stored bytes must match the source")
	}
}

// TestRewriteStoresB64JSON covers an explicit opt-in: the built-in rules skip
// b64_json, but a channel that selects it still gets the payload stored.
func TestRewriteStoresB64JSON(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	imageBytes := pngBytes()
	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t,
		`{"artifact_storage":{"enabled":true,"rewrite_paths":["$.data[*].b64_json"]}}`)
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	body := []byte(`{"data":[{"b64_json":"` + encoded + `"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	assert.NotContains(t, string(rewritten), encoded)
	assert.Contains(t, string(rewritten), "https://cdn.example.com/response/")
	assert.Equal(t, 1, storage.count())
}

func TestRewriteStoresDataURL(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	encoded := base64.StdEncoding.EncodeToString(pngBytes())
	body := []byte(`{"data":[{"url":"data:image/png;base64,` + encoded + `"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	assert.Contains(t, string(rewritten), "https://cdn.example.com/response/")
	assert.Equal(t, 1, storage.count())
}

// TestRewriteRefusesPrivateAddresses pins the SSRF guard: an upstream that points
// at the gateway's own network must not make it fetch anything.
func TestRewriteRefusesPrivateAddresses(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	imageBytes := pngBytes()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(imageBytes)
	}))
	t.Cleanup(cdn.Close)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	body := []byte(`{"data":[{"url":"` + cdn.URL + `/image.png"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	assert.False(t, changed, "a loopback address must not be fetched")
	assert.JSONEq(t, string(body), string(rewritten))
	assert.Equal(t, 0, storage.count())
}

func TestRewriteLeavesUnconfiguredChannelAlone(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t, "")
	body := []byte(`{"data":[{"url":"https://upstream.example.com/a.png"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	assert.False(t, changed)
	assert.JSONEq(t, string(body), string(rewritten))
	assert.Equal(t, 0, storage.count())
}

func TestRewriteLeavesNonJSONAlone(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	body := []byte("not json at all")
	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	assert.False(t, changed)
	assert.Equal(t, body, rewritten)
}

func TestRewriteFallsBackToPresignWithoutPublicBase(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)
	allowLoopbackFetches(t)

	imageBytes := pngBytes()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(imageBytes)
	}))
	t.Cleanup(cdn.Close)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	// No public base: the only address available is a signed one.
	pointStorageAt(t, storageServer.URL, "")

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	body := []byte(`{"data":[{"url":"` + cdn.URL + `/image.png"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	assert.Contains(t, string(rewritten), "X-Amz-Signature", "expected a presigned URL")
	assert.Equal(t, 1, storage.count())
}

func TestParseArtifactPath(t *testing.T) {
	cases := []struct {
		raw      string
		expected []pathSegment
	}{
		{"$.data[*].url", []pathSegment{
			{kind: segmentKey, key: "data"},
			{kind: segmentWildcard},
			{kind: segmentKey, key: "url"},
		}},
		{"$.data.0.url", []pathSegment{
			{kind: segmentKey, key: "data"},
			{kind: segmentIndex, index: 0},
			{kind: segmentKey, key: "url"},
		}},
		{"$.candidates[1].content.parts[0].inlineData.data", []pathSegment{
			{kind: segmentKey, key: "candidates"},
			{kind: segmentIndex, index: 1},
			{kind: segmentKey, key: "content"},
			{kind: segmentKey, key: "parts"},
			{kind: segmentIndex, index: 0},
			{kind: segmentKey, key: "inlineData"},
			{kind: segmentKey, key: "data"},
		}},
		{"$.content[*].source.data", []pathSegment{
			{kind: segmentKey, key: "content"},
			{kind: segmentWildcard},
			{kind: segmentKey, key: "source"},
			{kind: segmentKey, key: "data"},
		}},
	}
	for _, testCase := range cases {
		segments, err := parseArtifactPath(testCase.raw)
		require.NoError(t, err, testCase.raw)
		assert.Equal(t, testCase.expected, segments, testCase.raw)
	}
}

func TestParseArtifactPathRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"", "   ", "$", "$.", "$.a..b"} {
		_, err := parseArtifactPath(raw)
		assert.Error(t, err, "selector %q must be rejected", raw)
	}
}

// TestRewriteUsesCustomSelector proves the selector mode replaces the built-in
// matching rather than adding to it: a plain "url" outside the selector must be
// left alone.
func TestRewriteUsesCustomSelector(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t,
		`{"artifact_storage":{"enabled":true,"rewrite_paths":["$.result.media.data"]}}`)
	encoded := base64.StdEncoding.EncodeToString(pngBytes())
	body := []byte(`{"result":{"media":{"data":"` + encoded + `"}},"url":"https://untouched.example.com/x.png"}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	text := string(rewritten)
	assert.NotContains(t, text, encoded, "the selected field must be replaced")
	assert.Contains(t, text, "https://cdn.example.com/response/")
	assert.Contains(t, text, "https://untouched.example.com/x.png",
		"a field outside the selectors must stay untouched")
	assert.Equal(t, 1, storage.count())
}

// TestRewriteSelectorWildcardAndIndex covers the two addressing forms on a
// realistic Gemini-shaped body.
func TestRewriteSelectorWildcardAndIndex(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t,
		`{"artifact_storage":{"enabled":true,"rewrite_paths":["$.candidates[*].content.parts[*].inlineData.data"]}}`)
	first := base64.StdEncoding.EncodeToString(pngBytes())
	second := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x7f}, 4096)...))
	body := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` +
		first + `"}},{"inlineData":{"mimeType":"image/png","data":"` + second + `"}}]}}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	text := string(rewritten)
	assert.NotContains(t, text, first)
	assert.NotContains(t, text, second)
	assert.Equal(t, 2, storage.count(), "both parts must be stored")
}

// TestGlobalRewritePathsApplyToChannel covers the inheritance rule: a channel
// without its own selectors uses the gateway-wide default.
func TestGlobalRewritePathsApplyToChannel(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")
	withGlobalArtifactStorage(t, func(setting *system_setting.ArtifactStorageSetting) {
		setting.RewritePaths = []string{"$.result.media.data"}
	})

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	encoded := base64.StdEncoding.EncodeToString(pngBytes())
	body := []byte(`{"result":{"media":{"data":"` + encoded + `"}}}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	assert.NotContains(t, string(rewritten), encoded)
	assert.Equal(t, 1, storage.count())
}

// TestChannelRewritePathsReplaceGlobal makes sure a channel list is not merged
// with the default: a rule the channel did not declare must not fire.
func TestChannelRewritePathsReplaceGlobal(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")
	withGlobalArtifactStorage(t, func(setting *system_setting.ArtifactStorageSetting) {
		setting.RewritePaths = []string{"$.globalOnly.data"}
	})

	channelID := createRewriteTestChannel(t,
		`{"artifact_storage":{"enabled":true,"rewrite_paths":["$.channelOnly.data"]}}`)
	encoded := base64.StdEncoding.EncodeToString(pngBytes())
	body := []byte(`{"globalOnly":{"data":"` + encoded + `"},"channelOnly":{"data":"` + encoded + `"}}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	text := string(rewritten)
	assert.Equal(t, 1, storage.count(), "only the channel rule may fire")
	// The global-only field must keep its payload because that rule was replaced.
	assert.Contains(t, text, `"globalOnly":{"data":"`+encoded[:32])
}

// TestSelectorArrowWritesToSiblingField covers the form that makes a payload
// field safe to handle: the address goes to "url", the payload is cleared.
func TestSelectorArrowWritesToSiblingField(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t,
		`{"artifact_storage":{"enabled":true,"rewrite_paths":["$.data[*].b64_json => url clear"]}}`)
	encoded := base64.StdEncoding.EncodeToString(pngBytes())
	body := []byte(`{"data":[{"b64_json":"` + encoded + `"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	require.True(t, changed)
	text := string(rewritten)
	assert.NotContains(t, text, encoded, "the payload must be cleared")
	assert.Contains(t, text, `"url":"https://cdn.example.com/response/`)
	assert.Equal(t, 1, storage.count())
}

// TestB64JsonIgnoredByDefault pins the decision to leave b64_json alone unless a
// channel opts in: overwriting it with a URL breaks decoding clients.
func TestB64JsonIgnoredByDefault(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	storage := &rewriteFakeS3{objects: map[string][]byte{}}
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	pointStorageAt(t, storageServer.URL, "https://cdn.example.com")

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	encoded := base64.StdEncoding.EncodeToString(pngBytes())
	body := []byte(`{"data":[{"b64_json":"` + encoded + `"}]}`)

	rewritten, changed := RewriteResponseArtifacts(context.Background(), channelID, body)
	assert.False(t, changed, "b64_json must not be rewritten by the built-in rules")
	assert.JSONEq(t, string(body), string(rewritten))
	assert.Equal(t, 0, storage.count())
}

// TestRewriteInPlaceSkipsEventStream makes sure SSE bodies are never buffered.
func TestRewriteInPlaceSkipsEventStream(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	channelID := createRewriteTestChannel(t, `{"artifact_storage":{"enabled":true}}`)
	payload := "data: {\"url\":\"https://upstream.example.com/a.png\"}\n\n"
	response := &http.Response{
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          io.NopCloser(strings.NewReader(payload)),
		ContentLength: int64(len(payload)),
	}

	result := RewriteResponseArtifactsInPlace(context.Background(), channelID, response)
	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, string(body))
}

// TestRewriteInPlaceKeepsBodyReadableOnNoChange covers the replay path: a body
// that is read for inspection must still be delivered intact.
func TestRewriteInPlaceKeepsBodyReadableOnNoChange(t *testing.T) {
	setupRewriteTestDB(t)
	resetArtifactTargetHealth(t)

	channelID := createRewriteTestChannel(t, "")
	payload := `{"data":[{"url":"https://upstream.example.com/a.png"}]}`
	response := &http.Response{
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(payload)),
		ContentLength: int64(len(payload)),
	}

	result := RewriteResponseArtifactsInPlace(context.Background(), channelID, response)
	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, string(body))
}

// 上游把自己的字节一律标成 octet-stream（COS 直链常见）时，照抄会让图片在
// 严格客户端里变成"下载一个文件"，而对象 key 早就按魔数补了 .png —— 两边打架。
func TestContentTypeForSniffsGenericBinary(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + "0123456789abcdef")
	cases := []struct {
		header string
		want   string
	}{
		{"application/octet-stream", "image/png"},
		{"binary/octet-stream; charset=binary", "image/png"},
		{"", "image/png"},
		// 上游明确声明了具体类型就以它为准，不越权重判。
		{"image/jpeg", "image/jpeg"},
		{"text/plain; charset=utf-8", "text/plain; charset=utf-8"},
	}
	for _, c := range cases {
		if got := contentTypeFor(c.header, png); got != c.want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}
