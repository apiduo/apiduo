#!/usr/bin/env python3
"""Minimal OpenAI-compatible upstream used for local end-to-end checks.

Covers the surfaces the gateway relays in local regression:
  POST /v1/chat/completions   (json + stream)
  GET  /v1/models
  POST /v1/tools/search|fetch DeepSeek tool endpoints: raw body in, raw out
  POST /v1/files              multipart upload
  GET  /v1/files/{id}         object metadata
  GET  /v1/files/{id}/content original bytes
  DELETE /v1/files/{id}
"""
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = os.environ.get("MOCK_LOG", "/tmp/mock_upstream.log")

FILES = {}
FILE_SEQ = [0]


def log(line: str) -> None:
    with open(LOG, "a", encoding="utf-8") as fh:
        fh.write(line + "\n")


def parse_multipart(content_type: str, raw: bytes) -> dict:
    """Splits a multipart body into {field_name: bytes}. Enough for /v1/files."""
    marker = "boundary="
    index = content_type.find(marker)
    if index < 0:
        return {}
    boundary = content_type[index + len(marker):].strip().strip('"')
    delimiter = b"--" + boundary.encode()
    fields = {}
    for chunk in raw.split(delimiter):
        if not chunk.strip(b"\r\n-"):
            continue
        head, sep, body = chunk.partition(b"\r\n\r\n")
        if not sep:
            continue
        name = None
        for line in head.split(b"\r\n"):
            if b"content-disposition" in line.lower() and b'name="' in line:
                start = line.find(b'name="') + len(b'name="')
                name = line[start:line.find(b'"', start)].decode()
                break
        if name is None:
            continue
        fields[name] = body[:-2] if body.endswith(b"\r\n") else body
    return fields


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        log("ACCESS " + (fmt % args))

    def _json(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        # 模拟上游限流响应头，用于验证「满」维度采集链路。
        # 真实上游（OpenAI）在每次成功响应里都会带这组头。
        self.send_header("x-ratelimit-limit-requests", "10000")
        self.send_header("x-ratelimit-remaining-requests", "1200")
        self.send_header("x-ratelimit-reset-requests", "60s")
        self.end_headers()
        self.wfile.write(body)

    def _bytes(self, payload: bytes, content_type="application/octet-stream"):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _file_object(self, file_id, entry):
        return {
            "id": file_id,
            "object": "file",
            "bytes": len(entry["data"]),
            "created_at": entry["created_at"],
            "filename": entry["filename"],
            "purpose": entry["purpose"],
            "status": "processed",
        }

    def do_GET(self):
        log(f"GET {self.path}")
        path = self.path.split("?")[0]
        if path.rstrip("/").endswith("/v1/models"):
            self._json({"object": "list", "data": [{"id": "mock-gpt", "object": "model", "created": int(time.time()), "owned_by": "mock"}]})
            return
        if path.startswith("/v1/files/"):
            rest = path[len("/v1/files/"):]
            if rest.endswith("/content"):
                file_id = rest[: -len("/content")]
                entry = FILES.get(file_id)
                if entry is None:
                    self._json({"error": {"message": "no such file"}}, 404)
                    return
                self._bytes(entry["data"])
                return
            entry = FILES.get(rest)
            if entry is None:
                self._json({"error": {"message": "no such file"}}, 404)
                return
            self._json(self._file_object(rest, entry))
            return
        self._json({"error": {"message": "not found"}}, 404)

    def _read_body(self) -> bytes:
        """Reads the request body, decoding chunked framing when there is no length.

        The gateway streams a multipart upload through a pipe, so it arrives with
        Transfer-Encoding: chunked and no Content-Length. Reading only the length
        header silently yields an empty file and leaves the chunks in the socket,
        where the next request on that connection is parsed as garbage.
        """
        if (self.headers.get("Transfer-Encoding") or "").lower() == "chunked":
            body = bytearray()
            while True:
                size_line = self.rfile.readline().strip()
                if not size_line:
                    break
                size = int(size_line.split(b";")[0], 16)
                if size == 0:
                    self.rfile.readline()
                    break
                body.extend(self.rfile.read(size))
                self.rfile.read(2)
            return bytes(body)
        length = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(length) if length else b""

    def do_DELETE(self):
        log(f"DELETE {self.path}")
        path = self.path.split("?")[0]
        if path.startswith("/v1/files/"):
            file_id = path[len("/v1/files/"):]
            existed = FILES.pop(file_id, None) is not None
            self._json({"id": file_id, "object": "file", "deleted": existed})
            return
        self._json({"error": {"message": "not found"}}, 404)

    def do_POST(self):
        raw = self._read_body()
        path = self.path.split("?")[0]
        log(f"POST {path} auth={self.headers.get('Authorization', '')[:14]} bytes={len(raw)}")

        if path.rstrip("/") in ("/v1/tools/search", "/v1/tools/fetch"):
            # The gateway forwards the body verbatim and returns the reply as-is.
            try:
                request = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                request = {}
            self._json({"id": "tools-mock-1", "object": "list", "echo": request,
                        "results": [{"title": "mock result", "url": "https://example.com/mock"}]})
            return

        if path.rstrip("/") == "/v1/files":
            fields = parse_multipart(self.headers.get("Content-Type", ""), raw)
            data = fields.get("file", b"")
            FILE_SEQ[0] += 1
            file_id = f"file-mock-{FILE_SEQ[0]}"
            entry = {
                "data": data,
                "filename": fields.get("filename", b"upload.bin").decode(errors="replace"),
                "purpose": fields.get("purpose", b"").decode(errors="replace"),
                "created_at": int(time.time()),
            }
            FILES[file_id] = entry
            self._json(self._file_object(file_id, entry))
            return

        try:
            req = json.loads(raw or b"{}")
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
