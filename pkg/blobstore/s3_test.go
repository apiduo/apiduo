package blobstore

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeS3 answers the four verbs the backend uses. It deliberately does not
// verify signatures: the goal is to pin the request shape (path-style
// addressing, verb, headers) and the response parsing, not to re-test SigV4.
type fakeS3 struct {
	mu          sync.Mutex
	objects     map[string][]byte
	lastMethod  string
	lastPath    string
	lastHeaders http.Header
	lastQuery   url.Values
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastMethod = r.Method
	f.lastPath = r.URL.Path
	f.lastHeaders = r.Header.Clone()
	f.lastQuery = r.URL.Query()

	key := strings.TrimPrefix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.objects[key] = body
		digest := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("ETag", `"etag-value"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
			start, end, ok := parseTestRange(rangeHeader, len(data))
			if !ok {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(end)+"/"+strconv.Itoa(len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start : end+1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func parseTestRange(header string, size int) (int, int, bool) {
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return 0, 0, false
	}
	startText, endText, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false
	}
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end := size - 1
	if endText != "" {
		parsed, err := strconv.Atoi(endText)
		if err != nil || parsed < start {
			return 0, 0, false
		}
		if parsed < end {
			end = parsed
		}
	}
	return start, end, true
}

func newTestBackend(t *testing.T) (Backend, *fakeS3) {
	t.Helper()
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	config := Config{
		Endpoint:  server.URL,
		Bucket:    "apiduo-artifacts",
		Region:    "us-east-1",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
		Prefix:    "gateway",
		PathStyle: true,
	}
	backend, err := NewS3(config)
	require.NoError(t, err)
	return backend, fake
}

func TestS3PutUsesPathStyleAndStoresBytes(t *testing.T) {
	backend, fake := newTestBackend(t)
	payload := []byte("artifact-bytes")

	ref, err := backend.Put(context.Background(), "tasks/t1/video", strings.NewReader(string(payload)), PutOptions{
		ContentType:   "video/mp4",
		ContentLength: int64(len(payload)),
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, fake.lastMethod)
	// Path-style addressing is the MinIO contract: bucket first, then the key.
	assert.Equal(t, "/apiduo-artifacts/gateway/tasks/t1/video", fake.lastPath)
	assert.Equal(t, "video/mp4", fake.lastHeaders.Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(len(payload)), fake.lastHeaders.Get("Content-Length"))
	assert.Equal(t, int64(len(payload)), ref.Size)
	assert.Equal(t, "apiduo-artifacts", ref.Bucket)
	assert.NotEmpty(t, fake.objects["apiduo-artifacts/gateway/tasks/t1/video"])
}

func TestS3PutSpoolsUnknownLength(t *testing.T) {
	backend, fake := newTestBackend(t)
	payload := strings.Repeat("x", 4096)

	// ContentLength zero means "unknown" and forces the spool path, which must
	// still produce a correctly sized object.
	_, err := backend.Put(context.Background(), "tasks/t2/file", strings.NewReader(payload), PutOptions{
		ContentType: "application/octet-stream",
	})
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(len(payload)), fake.lastHeaders.Get("Content-Length"))
}

func TestS3StatMissingObjectIsNotFound(t *testing.T) {
	backend, _ := newTestBackend(t)

	_, err := backend.Stat(context.Background(), "tasks/missing/video")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestS3OpenPassesRangeAndReportsTotalSize(t *testing.T) {
	backend, _ := newTestBackend(t)
	payload := []byte("0123456789abcdef")
	_, err := backend.Put(context.Background(), "tasks/t3/video", strings.NewReader(string(payload)), PutOptions{
		ContentType:   "video/mp4",
		ContentLength: int64(len(payload)),
	})
	require.NoError(t, err)

	body, ref, err := backend.Open(context.Background(), "tasks/t3/video", "bytes=4-7")
	require.NoError(t, err)
	defer body.Close()

	chunk, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, "4567", string(chunk))
	// ContentLength is the slice, Size is the whole object; a proxy needs both.
	assert.Equal(t, int64(4), ref.ContentLength)
	assert.Equal(t, int64(len(payload)), ref.Size)
	assert.Equal(t, "bytes 4-7/16", ref.ContentRange)
}

func TestS3PresignProducesSignedPathStyleURL(t *testing.T) {
	backend, _ := newTestBackend(t)

	presigned, err := backend.PresignGet(context.Background(), "tasks/t4/video", time.Minute)
	require.NoError(t, err)

	parsed, err := url.Parse(presigned)
	require.NoError(t, err)
	assert.Equal(t, "/apiduo-artifacts/gateway/tasks/t4/video", parsed.Path)
	assert.NotEmpty(t, parsed.Query().Get("X-Amz-Signature"))
	assert.NotEmpty(t, parsed.Query().Get("X-Amz-Expires"))
}

func TestS3DeleteThenStatMisses(t *testing.T) {
	backend, _ := newTestBackend(t)
	payload := []byte("x")
	_, err := backend.Put(context.Background(), "tasks/t5/video", strings.NewReader(string(payload)), PutOptions{
		ContentLength: int64(len(payload)),
	})
	require.NoError(t, err)

	require.NoError(t, backend.Delete(context.Background(), "tasks/t5/video"))
	_, err = backend.Stat(context.Background(), "tasks/t5/video")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestS3RejectsTraversalKeyBeforeSending(t *testing.T) {
	backend, fake := newTestBackend(t)
	_, err := backend.Put(context.Background(), "../../etc/passwd", strings.NewReader("x"), PutOptions{ContentLength: 1})
	require.Error(t, err)
	assert.Empty(t, fake.lastMethod, "no request should reach the server")
}
