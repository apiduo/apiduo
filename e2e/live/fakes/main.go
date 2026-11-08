// 假上游（OpenAI 兼容）+ 假 Langfuse OTLP sink，供 e2e/live 使用。
//
// upstream /v1/chat/completions：流式 + 非流式；user 消息以 fail 开头时返回 500。
// sink     /api/public/otel/v1/traces：每个请求体落一个文件，另有 /count 供断言。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var sinkSeq atomic.Int64

// leakMarker is deliberately fake-but-shaped-like-real so redaction has
// something to hit.
const leakMarker = "sk-live-SECRETVALUE0123456789"

func main() {
	upstreamAddr := flag.String("upstream", "127.0.0.1:39101", "fake upstream addr")
	sinkAddr := flag.String("sink", "127.0.0.1:39102", "fake langfuse addr")
	sinkDir := flag.String("dir", "/tmp/apiduo-e2e/sink", "sink output dir")
	headersPath := flag.String("headers", "/tmp/apiduo-e2e/upstream-headers.log", "record traceparent per upstream call")
	flag.Parse()
	_ = os.MkdirAll(*sinkDir, 0o755)

	up := http.NewServeMux()
	up.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model"}]}`)
	})
	up.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Record what the gateway actually sent upstream so the "disabled means
		// byte-identical outbound" claim can be checked, not assumed.
		if f, err := os.OpenFile(*headersPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "traceparent=%q tracestate=%q\n", r.Header.Get("traceparent"), r.Header.Get("tracestate"))
			f.Close()
		}
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)

		// "fail" in the last user message makes the upstream 500, so the
		// failure path (relay defer, no usage) can be exercised.
		fail := false
		for _, m := range req.Messages {
			if m.Role == "user" && len(m.Content) >= 4 && m.Content[:4] == "fail" {
				fail = true
			}
		}
		if fail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"upstream exploded on purpose","type":"server_error"}}`)
			return
		}

		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"chatcmpl-nonstream","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"北京明天多云转晴。`+leakMarker+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":21,"completion_tokens":13,"total_tokens":34}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)
		emit := func(s string) {
			fmt.Fprintf(w, "data: %s\n\n", s)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(15 * time.Millisecond)
		}

		emit(`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		emit(`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"北京明天多云转晴"},"finish_reason":null}]}`)
		emit(`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather_lookup","arguments":"{\"city\":\"北"}}]},"finish_reason":null}]}`)
		emit(`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"京\"}"}}]},"finish_reason":null}]}`)
		emit(`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"。` + leakMarker + `"},"finish_reason":null}]}`)
		emit(`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":27,"completion_tokens":19,"total_tokens":46}}`)
		io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	sink := http.NewServeMux()
	sink.HandleFunc("/api/public/otel/v1/traces", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := sinkSeq.Add(1)
		base := filepath.Join(*sinkDir, fmt.Sprintf("%03d", n))
		_ = os.WriteFile(base+".json", body, 0o644)
		meta := fmt.Sprintf("auth=%s\nversion=%s\nctype=%s\nbytes=%d\n",
			r.Header.Get("Authorization"), r.Header.Get("x-langfuse-ingestion-version"),
			r.Header.Get("Content-Type"), len(body))
		_ = os.WriteFile(base+".meta", []byte(meta), 0o644)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"partialSuccess":{}}`)
	})
	sink.HandleFunc("/api/public/projects", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"e2e-project"}]}`)
	})
	sink.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, fmt.Sprintf("%d", sinkSeq.Load()))
	})

	go func() {
		log.Println("sink on", *sinkAddr)
		if err := http.ListenAndServe(*sinkAddr, sink); err != nil {
			log.Fatal(err)
		}
	}()
	log.Println("upstream on", *upstreamAddr)
	if err := http.ListenAndServe(*upstreamAddr, up); err != nil {
		log.Fatal(err)
	}
}
