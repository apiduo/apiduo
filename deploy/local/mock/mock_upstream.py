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
import threading
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = os.environ.get("MOCK_LOG", "/tmp/mock_upstream.log")

FILES = {}
FILE_SEQ = [0]

# 异步图像任务：提交回任务号，查询第一次 RUNNING、第二次起 SUCCEEDED。
# 让轮询至少跑一轮，才验证得到状态流转，而不是「提交即完成」。
TASK_SEQ = [0]
FLAKY = [0]
# 观测 worker 的并发：当前在飞数与本轮峰值。
SLOW_ACTIVE = [0]
SLOW_MAX = [0]
SLOW_LOCK = threading.Lock()
TASK_POLLS = {}
# 任务型上游回报的 token 数。按 duration 折算（每秒 24000），这样「按 token
# 结算」读到的是只有完成回包才有的数字，客户端无法在提交时自己算出来。
TASK_TOKENS = {}


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
        if path.rstrip("/") == "/mock/stats":
            with SLOW_LOCK:
                self._json({"active": SLOW_ACTIVE[0], "max_concurrency": SLOW_MAX[0]})
            return
        if path.startswith("/mock/images/query/"):
            task_id = path[len("/mock/images/query/"):]
            polls = TASK_POLLS.get(task_id, 0) + 1
            TASK_POLLS[task_id] = polls
            if polls < 2:
                self._json({"task_id": task_id, "status": "RUNNING"})
                return
            self._json({
                "task_id": task_id,
                "status": "SUCCEEDED",
                "url": f"http://mock-upstream:18080/mock/out/{task_id}.png",
            })
            return
        if path.startswith("/mock/tasks/query/"):
            task_id = path[len("/mock/tasks/query/"):]
            polls = TASK_POLLS.get(task_id, 0) + 1
            TASK_POLLS[task_id] = polls
            if polls < 2:
                self._json({"task_id": task_id, "status": "RUNNING"})
                return
            # 终态才带 usage：客户端/网关在轮询阶段读不到 token 数。
            self._json({
                "task_id": task_id,
                "status": "SUCCEEDED",
                "url": f"http://mock-upstream:18080/mock/out/{task_id}.mp4",
                "usage": {"total_tokens": TASK_TOKENS.get(task_id, 0)},
            })
            return
        if path.startswith("/mock/tasks/query-nousage/"):
            # 完成回包不带 usage：运营配了 total_tokens_path、上游却没报用量时
            # 的样子。按 token 的表达式在这种回包下会读到 tokens=0。
            task_id = path[len("/mock/tasks/query-nousage/"):]
            polls = TASK_POLLS.get(task_id, 0) + 1
            TASK_POLLS[task_id] = polls
            if polls < 2:
                self._json({"task_id": task_id, "status": "RUNNING"})
                return
            self._json({
                "task_id": task_id,
                "status": "SUCCEEDED",
                "url": f"http://mock-upstream:18080/mock/out/{task_id}.mp4",
            })
            return
        if path.startswith("/mock/bad-url/query/"):
            task_id = path[len("/mock/bad-url/query/"):]
            self._json({
                "task_id": task_id,
                "status": "SUCCEEDED",
                # A hostile result url: a client that renders it as a link turns
                # the gateway into an XSS delivery channel.
                "url": "javascript:alert(document.cookie)",
            })
            return
        if path.startswith("/mock/out/"):
            # Task adaptors point finished results here. Serving real bytes is
            # what lets an end-to-end media download check content, not just a
            # URL shape.
            body = b"apiduo-mock-media"
            self.send_response(200)
            self.send_header("Content-Type", "video/mp4")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)
            return
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

        if path.rstrip("/") == "/v1/images/generations":
            try:
                request = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                request = {}
            log(f"sync image generation model={request.get('model')} prompt={request.get('prompt')}")
            self._json({
                "created": int(time.time()),
                "data": [{"url": "http://mock-upstream:18080/mock/out/sync.png"}],
            })
            return

        # A vendor endpoint of its own, reachable only through a channel that
        # declares it as its primary endpoint. The distinct result url is how a
        # test tells which address the gateway actually used.
        if path.rstrip("/") == "/api/v3/images/generations":
            try:
                request = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                request = {}
            log(f"vendor image generation model={request.get('model')} prompt={request.get('prompt')}")
            self._json({
                "created": int(time.time()),
                "data": [{"url": "http://mock-upstream:18080/mock/out/vendor.png"}],
            })
            return

        if path.rstrip("/") == "/mock/images/slow":
            # A synchronous image that takes real time. The worker's scheduling
            # behavior is only observable when a task is not instantaneous.
            try:
                request = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                request = {}
            delay = float(request.get("delay") or os.environ.get("MOCK_SLOW_SECONDS", "20"))
            with SLOW_LOCK:
                SLOW_ACTIVE[0] += 1
                SLOW_MAX[0] = max(SLOW_MAX[0], SLOW_ACTIVE[0])
            log(f"slow image start model={request.get('model')} delay={delay}")
            time.sleep(delay)
            with SLOW_LOCK:
                SLOW_ACTIVE[0] -= 1
            log(f"slow image done model={request.get('model')}")
            self._json({
                "created": int(time.time()),
                "data": [{"url": "http://mock-upstream:18080/mock/out/slow.png"}],
            })
            return

        if path.rstrip("/") == "/mock/reset":
            # 计数器是进程全局的；场景之间要能各自从零开始。
            FLAKY[0] = 0
            TASK_SEQ[0] = 0
            with SLOW_LOCK:
                SLOW_ACTIVE[0] = 0
                SLOW_MAX[0] = 0
            TASK_POLLS.clear()
            TASK_TOKENS.clear()
            self._json({"reset": True})
            return

        if path.rstrip("/") == "/mock/images/denied":
            # 永远限流：用来验证「换渠道」而不是「原地重试」。
            log("denied image rate limited")
            body = b'{"error":{"message":"rate limited"}}'
            self.send_response(429)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return

        if path.rstrip("/") == "/mock/images/flaky":
            # 前两次限流，第三次放行：验证 worker 对 429 的重试。
            FLAKY[0] += 1
            if FLAKY[0] <= 2:
                log(f"flaky image rate limited (attempt {FLAKY[0]})")
                body = b'{"error":{"message":"rate limited"}}'
                self.send_response(429)
                self.send_header("Content-Type", "application/json")
                # 不发 Retry-After：这个端点用来验证 worker 自己的指数退避。
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return
            log(f"flaky image served (attempt {FLAKY[0]})")
            self._json({
                "created": int(time.time()),
                "data": [{"url": "http://mock-upstream:18080/mock/out/flaky.png"}],
            })
            return

        if path.rstrip("/") == "/mock/fail-500":
            log("mock failure endpoint hit")
            self._json({"error": {"message": "mock upstream failure"}}, 500)
            return

        if path.rstrip("/") == "/mock/tasks/submit":
            try:
                request = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                request = {}
            TASK_SEQ[0] += 1
            task_id = f"mock-task-{TASK_SEQ[0]}"
            TASK_POLLS[task_id] = 0
            try:
                seconds = float(request.get("duration") or 5)
            except (TypeError, ValueError):
                seconds = 5.0
            TASK_TOKENS[task_id] = int(seconds * 24000)
            log(f"submit task {task_id} model={request.get('model')} duration={seconds} tokens={TASK_TOKENS[task_id]}")
            self._json({"task_id": task_id, "status": "QUEUED"})
            return

        if path.rstrip("/") == "/mock/images/submit":
            try:
                request = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                request = {}
            TASK_SEQ[0] += 1
            task_id = f"mock-image-{TASK_SEQ[0]}"
            TASK_POLLS[task_id] = 0
            log(f"submit image task {task_id} model={request.get('model')} prompt={request.get('prompt')}")
            # 受理即回任务号：与真实异步上游一致，不含任何结果字段。
            self._json({"task_id": task_id, "status": "QUEUED"})
            return

        if path.rstrip("/") == "/mock/bad-url/submit":
            TASK_SEQ[0] += 1
            task_id = f"bad-url-{TASK_SEQ[0]}"
            TASK_POLLS[task_id] = 0
            self._json({"task_id": task_id, "status": "QUEUED"})
            return

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
