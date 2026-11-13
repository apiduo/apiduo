#!/usr/bin/env python3
"""Minimal OpenAI-compatible upstream used for local end-to-end checks."""
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = os.environ.get("MOCK_LOG", "/tmp/mock_upstream.log")


def log(line: str) -> None:
    with open(LOG, "a", encoding="utf-8") as fh:
        fh.write(line + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        log("ACCESS " + (fmt % args))

    def _json(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        log(f"GET {self.path}")
        if self.path.rstrip("/").endswith("/v1/models"):
            self._json({"object": "list", "data": [{"id": "mock-gpt", "object": "model", "created": int(time.time()), "owned_by": "mock"}]})
            return
        self._json({"error": {"message": "not found"}}, 404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length).decode()
        log(f"POST {self.path} auth={self.headers.get('Authorization', '')[:14]} body={raw[:400]}")

        try:
            req = json.loads(raw or "{}")
        except json.JSONDecodeError:
            req = {}

        model = req.get("model", "mock-gpt")
        stream = bool(req.get("stream"))
        text = "pong from mock upstream"
        prompt_tokens = sum(len(str(m.get("content", ""))) for m in req.get("messages", [])) // 4 + 1
        completion_tokens = len(text) // 4 + 1

        if not stream:
            self._json({
                "id": "chatcmpl-mock-1",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": model,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens, "total_tokens": prompt_tokens + completion_tokens},
            })
            return

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        for piece in ("pong ", "from ", "mock ", "upstream"):
            chunk = {
                "id": "chatcmpl-mock-1",
                "object": "chat.completion.chunk",
                "created": int(time.time()),
                "model": model,
                "choices": [{"index": 0, "delta": {"content": piece}, "finish_reason": None}],
            }
            self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
            self.wfile.flush()
            time.sleep(0.01)
        final = {
            "id": "chatcmpl-mock-1",
            "object": "chat.completion.chunk",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens, "total_tokens": prompt_tokens + completion_tokens},
        }
        self.wfile.write(f"data: {json.dumps(final)}\n\n".encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    open(LOG, "w").close()
    ThreadingHTTPServer((os.environ.get("MOCK_HOST", "127.0.0.1"), int(os.environ.get("MOCK_PORT", "18080"))), Handler).serve_forever()
