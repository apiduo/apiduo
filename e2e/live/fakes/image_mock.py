"""记录式假上游：每条请求落一行 JSON（path / content_type / 字段 / 文件 part 指纹）。

图片协议的断言全靠它——「网关说它发了什么」没有意义，只有上游收到什么才算数。"""

import argparse
import hashlib
import json
import re
import time
from email.parser import BytesParser
from email.policy import default as email_policy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SEQ = {"n": 0}


def record(entry):
    with open(LOG, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(entry, ensure_ascii=False) + "\n")


def parse_multipart(content_type, raw):
    message = BytesParser(policy=email_policy).parsebytes(
        b"Content-Type: " + content_type.encode() + b"\r\nMIME-Version: 1.0\r\n\r\n" + raw
    )
    files, fields = [], {}
    for part in message.iter_parts():
        name = part.get_param("name", header="content-disposition")
        filename = part.get_filename()
        payload = part.get_payload(decode=True) or b""
        if filename:
            files.append(
                {
                    "field": name,
                    "filename": filename,
                    "bytes": len(payload),
                    "sha8": hashlib.sha256(payload).hexdigest()[:8],
                }
            )
        else:
            fields[name] = payload.decode("utf-8", "replace")
    return files, fields


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length)
        content_type = self.headers.get("Content-Type") or ""
        SEQ["n"] += 1
        entry = {
            "seq": SEQ["n"],
            "at": time.strftime("%H:%M:%S"),
            "path": self.path,
            "content_type": content_type.split(";")[0],
            "bytes": len(raw),
        }
        if content_type.startswith("multipart/form-data"):
            files, fields = parse_multipart(content_type, raw)
            entry.update({"files": files, "fields": fields})
        else:
            text = raw.decode("utf-8", "replace")
            entry["body"] = text[:2000]
            # 折叠后的 body 可能上百 KB（data URI 在前），截断后不是合法 JSON；
            # 用全量文本提摘要，供测试断言用。
            try:
                body = json.loads(text)
            except Exception:
                body = None
            if isinstance(body, dict):
                image = body.get("image")
                if isinstance(image, list):
                    first = image[0] if image else None
                    count = len(image)
                else:
                    first = image
                    count = 1 if image else 0
                entry["json"] = {
                    "keys": sorted(body.keys()),
                    "prompt": body.get("prompt"),
                    "model": body.get("model"),
                    "image_prefix": first[:32] if isinstance(first, str) else None,
                    "image_count": count,
                    "n": body.get("n"),
                    "has_mask": "mask" in body,
                }
                entry["prompt_hint"] = body.get("prompt")
        record(entry)

        # /pg/chat/completions（MCP 表单的「一键补全」）走的是 chat 形状：
        # 回一份固定 JSON，让 e2e 能确认「AI 路径」真的被解析并填进了表单，
        # 而不是每次都悄悄降级到本地规则还以为通过了。
        if "/chat/completions" in self.path:
            content = json.dumps({
                "summary": "e2e 图像模型：文生图 / 图生图",
                "tags": ["文生图", "图生图", "多参考图"],
                "async": False,
                "max_ref_images": 2,
                "max_ref_videos": None,
                "max_prompt_chars": 1000,
                "parameters": {"size": {"enum": ["1K", "2K"], "default": "1K"}},
                "confidence": "high",
                "notes": "mock assistant",
            }, ensure_ascii=False)
            payload = json.dumps({
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "created": 1700000000,
                "model": entry.get("json", {}).get("model") or "mock",
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": content},
                    "finish_reason": "stop",
                }],
                "usage": {"prompt_tokens": 10, "completion_tokens": 50,
                          "total_tokens": 60},
            }).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return

        payload = json.dumps(
            {
                "created": 1700000000,
                "data": [
                    {"url": "https://cdn.example.com/mock.png", "revised_prompt": "rp"}
                ],
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):
        pass


class Server(ThreadingHTTPServer):
    # 默认 backlog=5，8 并发会有一两个连接被拒（表现为 do request failed），调大再测。
    request_queue_size = 128
    daemon_threads = True


_ap = argparse.ArgumentParser()
_ap.add_argument("--port", type=int, default=39403)
_ap.add_argument("--log", default="/tmp/apiduo-e2e/image.log")
_a = _ap.parse_args()
LOG = _a.log

Server(("127.0.0.1", _a.port), Handler).serve_forever()
