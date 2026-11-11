package trace

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeLangfuse serves the two hops LoadMedia makes: the metadata lookup that
// returns a presigned object URL, and the object itself.
func fakeLangfuse(t *testing.T, raw []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var lookups atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/public/media/abc123xyz":
			lookups.Add(1)
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "no auth", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mediaId":     "abc123xyz",
				"contentType": "image/png",
				"url":         "http://" + r.Host + "/object",
			})
		case "/object":
			// S3 rejects a presigned GET that carries our Basic auth.
			if r.Header.Get("Authorization") != "" {
				http.Error(w, "unexpected auth on presigned hop", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &lookups
}

func TestLoadMediaResolvesAndCaches(t *testing.T) {
	resetMediaState()
	raw := noisePNG(t, 800, 600)
	srv, lookups := fakeLangfuse(t, raw)
	setConfig(&Config{Host: srv.URL, PublicKey: "pk-lf-test", SecretKey: "sk-lf-test"})
	t.Cleanup(func() { setConfig(&Config{}) })

	data, mime, err := LoadMedia("abc123xyz", 512)
	if err != nil {
		t.Fatalf("LoadMedia: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("thumb mime = %s", mime)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("thumb undecodable: %v", err)
	}
	if cfg.Width != 512 {
		t.Fatalf("thumb width = %d", cfg.Width)
	}

	// Second call must be served from the LRU: same bytes, no new lookup.
	again, _, err := LoadMedia("abc123xyz", 512)
	if err != nil || !bytes.Equal(again, data) {
		t.Fatalf("cached read differs (err=%v)", err)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("langfuse lookups = %d, want 1 (second read must hit the cache)", got)
	}

	// Different width is a different cache entry.
	if _, _, err := LoadMedia("abc123xyz", 256); err != nil {
		t.Fatalf("LoadMedia 256: %v", err)
	}
	if got := lookups.Load(); got != 2 {
		t.Fatalf("langfuse lookups after width change = %d, want 2", got)
	}
}

func TestLoadMediaOriginalWhenNoWidth(t *testing.T) {
	resetMediaState()
	raw := noisePNG(t, 64, 64)
	srv, _ := fakeLangfuse(t, raw)
	setConfig(&Config{Host: srv.URL, PublicKey: "pk-lf-test", SecretKey: "sk-lf-test"})
	t.Cleanup(func() { setConfig(&Config{}) })

	data, mime, err := LoadMedia("abc123xyz", 0)
	if err != nil {
		t.Fatalf("LoadMedia: %v", err)
	}
	if mime != "image/png" || !bytes.Equal(data, raw) {
		t.Fatalf("original not returned verbatim: mime=%s equal=%v", mime, bytes.Equal(data, raw))
	}
}

func TestLoadMediaRejectsBadID(t *testing.T) {
	resetMediaState()
	setConfig(&Config{Host: "http://127.0.0.1:1", PublicKey: "p", SecretKey: "s"})
	t.Cleanup(func() { setConfig(&Config{}) })
	if _, _, err := LoadMedia("../etc/passwd", 0); err == nil {
		t.Fatal("path-like id must be rejected before any request")
	}
}
