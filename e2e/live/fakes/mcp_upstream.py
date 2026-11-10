"""MCP 套件专用假上游：图片同步 + 视频异步两套形状，并记录上游实际收到的字节。

为什么不用 image_mock.py：那个对所有 POST 都回图片形状，视频的
POST/GET 任务链就断了。这里把 task 状态机做出来，才能验证
「MCP 签发的 task_id → apiduo 任务查询 → 上游任务 id」这条链没串位。

控制面（只在本机测试用）：
  POST /__control   {"image_status":400,"image_body":"...","video_status":500,...}
  GET  /__log       返回全部入站记录，套件据此断言「发出去的到底是什么」
  POST /__reset     清空记录与行为
"""

import argparse
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
STATE = {"seq": 0, "rows": [], "image_status": 200, "image_body": None,
         "video_status": 200, "video_body": None,
         "query_status": 200, "query_body": None}


def record(entry):
    with LOCK:
        STATE["seq"] += 1
        entry["seq"] = STATE["seq"]
        entry["at"] = time.strftime("%H:%M:%S")
        STATE["rows"].append(entry)


def snapshot():
    with LOCK:
        return {"rows": list(STATE["rows"]), "seq": STATE["seq"]}


def behavior():
    with LOCK:
        return {k: STATE[k] for k in
                ("image_status", "image_body", "video_status", "video_body",
                 "query_status", "query_body")}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def body(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            return raw, json.loads(raw) if raw else {}
        except Exception:
            return raw, {"__raw": raw.decode("utf-8", "replace")[:2000]}

    def send_json(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        raw, body = self.body()
        record({"method": "POST", "path": self.path, "body": body,
                "bytes": len(raw), "auth": self.headers.get("Authorization")})

        if self.path == "/__reset":
            with LOCK:
                STATE["rows"] = []
                for key in ("image_status", "image_body", "video_status", "video_body",
                            "query_status", "query_body"):
                    STATE[key] = 200 if key.endswith("status") else None
            return self.send_json(200, {"ok": True})

        if self.path == "/__control":
            with LOCK:
                for key in ("image_status", "image_body", "video_status", "video_body",
                            "query_status", "query_body"):
                    if key in body:
                        STATE[key] = body[key]
            return self.send_json(200, {"ok": True, "behavior": behavior()})

        if self.path.startswith("/v1/images/generations"):
            b = behavior()
            if b["image_status"] != 200:
                return self.send_json(b["image_status"], b["image_body"] or {"error": {"message": "image boom"}})
            return self.send_json(200, {
                "created": 1730000000,
                "data": [{"url": "https://cdn.example.com/mcp-%d.png" % STATE["seq"],
                          "mime_type": "image/png"}],
            })

        if self.path.startswith("/v1/video/generations"):
            b = behavior()
            if b["video_status"] != 200:
                return self.send_json(b["video_status"], b["video_body"] or {"error": {"message": "video boom"}})
            return self.send_json(200, {"task_id": "up-%d" % STATE["seq"], "status": "queued"})

        return self.send_json(404, {"error": {"message": "no route " + self.path}})

    def do_GET(self):
        if self.path == "/__log":
            return self.send_json(200, snapshot())
        if self.path.startswith("/v1/video/generations/"):
            task_id = self.path.rsplit("/", 1)[-1]
            record({"method": "GET", "path": self.path, "task_id": task_id,
                    "auth": self.headers.get("Authorization")})
            b = behavior()
            if b["query_status"] != 200:
                return self.send_json(b["query_status"], b["query_body"] or {"error": {"message": "query boom"}})
            return self.send_json(200, {"task_id": task_id, "status": "succeeded",
                                        "url": "https://cdn.example.com/mcp.mp4", "format": "mp4"})
        return self.send_json(404, {"error": {"message": "no route " + self.path}})

    def log_message(self, *args):
        pass


class Server(ThreadingHTTPServer):
    request_queue_size = 128
    daemon_threads = True


ap = argparse.ArgumentParser()
ap.add_argument("--port", type=int, default=39430)
args = ap.parse_args()
Server(("127.0.0.1", args.port), Handler).serve_forever()
