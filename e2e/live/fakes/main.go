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
	"strings"
	"sync/atomic"
	"time"
)

var sinkSeq atomic.Int64

// leakMarker is deliberately fake-but-shaped-like-real so redaction has
// something to hit.
const leakMarker = "sk-live-SECRETVALUE0123456789"

// shortKey trims the scheme and the fixed e2e prefix so the log stays
// readable and never carries a full credential-shaped string.
func shortKey(auth string) string {
	auth = strings.TrimPrefix(auth, "Bearer ")
	return strings.TrimPrefix(auth, "sk-e2e-")
}

func main() {
	upstreamAddr := flag.String("upstream", "127.0.0.1:39101", "fake upstream addr")
	sinkAddr := flag.String("sink", "127.0.0.1:39102", "fake langfuse addr")
	sinkDir := flag.String("dir", "/tmp/apiduo-e2e/sink", "sink output dir")
	headersPath := flag.String("headers", "/tmp/apiduo-e2e/upstream-headers.log", "record traceparent per upstream call")
	// 内容触发失败的开关。第二台假上游用 -fail-prefix= 关掉它，否则
	// "跨分组重试" 用例里本该成功的那台也会跟着 500，测出来全是失败。
	failPrefix := flag.String("fail-prefix", "fail", "user content starting with this makes the upstream 500; empty disables")
	// 上游实际收到的报文。参数覆盖类断言必须看这个：网关自己的日志只能
	// 证明它"以为"发了什么。
	bodiesPath := flag.String("bodies", "/tmp/apiduo-e2e/upstream-bodies.log", "record request bodies, one JSON per line")
	flag.Parse()
	_ = os.MkdirAll(*sinkDir, 0o755)

	up := http.NewServeMux()
	up.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model"}]}`)
	})
	up.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var probe struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &probe)
		reqModel := probe.Model
		// Record what the gateway actually sent upstream so the "disabled means
		// byte-identical outbound" claim can be checked, not assumed.
		if f, err := os.OpenFile(*headersPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			// model 也记下来：多渠道选择用例要按模型统计「哪台上游被选中了多少次」
			// auth 同理：多 key 渠道只有上游知道这次用的是哪把 key，网关自报的
			// multi_key_index 不能当证据。这里只留去掉固定前缀的后半段。
			fmt.Fprintf(f, "model=%q path=%q auth=%q traceparent=%q tracestate=%q\n",
				reqModel, r.URL.Path, shortKey(r.Header.Get("Authorization")),
				r.Header.Get("traceparent"), r.Header.Get("tracestate"))
			f.Close()
		}
		if f, err := os.OpenFile(*bodiesPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			rec, _ := json.Marshal(map[string]string{"model": reqModel, "body": string(body)})
			f.Write(append(rec, '\n'))
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
		if *failPrefix != "" {
			for _, m := range req.Messages {
				if m.Role == "user" && strings.HasPrefix(m.Content, *failPrefix) {
					fail = true
				}
			}
		}
		// 以 -bad 结尾的 key 一律 500：多 key 渠道要能证明「坏 key 只影响它自己」
		if strings.Contains(shortKey(r.Header.Get("Authorization")), "bad") {
			fail = true
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
