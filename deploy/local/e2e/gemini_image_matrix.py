#!/usr/bin/env python3
"""Gemini 图片协议 5×3 矩阵端到端判据。

依赖本地栈与三条渠道：
  gemini：models=xx，image_protocol.upstream=gemini，model_mapping xx→mock-gemini
  ark   ：models=yy，image_protocol.upstream=ark
  openai：models=zz，image_protocol.upstream=openai

用法：
  BASE=http://127.0.0.1:39047 TOKEN_KEY=... python3 gemini_image_matrix.py
可选：
  MOCK=http://127.0.0.1:18080

判据分三组：
  T1  Gemini 上游 × 五个入口
  T2  非 Gemini 上游（ark/openai）× Gemini 原生入口
  T3  三上游的回包字段集合与响应头一致
"""
import base64
import json
import os
import sys
import urllib.error
import urllib.request
import uuid

BASE = os.environ.get("BASE", "http://127.0.0.1:39047").rstrip("/")
MOCK = os.environ.get("MOCK", "http://127.0.0.1:18080").rstrip("/")
KEY = os.environ.get("TOKEN_KEY", "").strip()
if not KEY:
    sys.exit("TOKEN_KEY is required")

PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="
)

ok = 0
failed = []


def check(name, cond, detail=""):
    global ok
    if cond:
        ok += 1
        print(f"  PASS {name}")
        return True
    failed.append(name)
    print(f"  FAIL {name}  {detail}")
    return False


def http(method, path, body=None, ctype="application/json", base=None):
    url = (base or BASE) + path
    data = body
    if isinstance(body, dict):
        data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Bearer " + KEY)
    if data is not None:
        req.add_header("Content-Type", ctype)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as err:
        return err.code, dict(err.headers), err.read()


def http_json(method, path, body=None, base=None):
    status, headers, raw = http(method, path, body, base=base)
    try:
        return status, headers, json.loads(raw or b"{}")
    except json.JSONDecodeError:
        return status, headers, {"_raw": raw.decode("utf-8", "replace")}


def mock_last():
    status, _, body = http_json("GET", "/mock/last-gemini-image", base=MOCK)
    return body if status == 200 else {}


def multipart(fields, files):
    boundary = "----apiduo" + uuid.uuid4().hex
    out = bytearray()
    for name, value in fields.items():
        out += f"--{boundary}\r\n".encode()
        out += f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode()
        out += str(value).encode() + b"\r\n"
    for name, (filename, blob, ctype) in files.items():
        out += f"--{boundary}\r\n".encode()
        out += (
            f'Content-Disposition: form-data; name="{name}"; filename="{filename}"\r\n'
            f"Content-Type: {ctype}\r\n\r\n"
        ).encode()
        out += blob + b"\r\n"
    out += f"--{boundary}--\r\n".encode()
    return bytes(out), f"multipart/form-data; boundary={boundary}"


def sse_events(raw):
    events = []
    for block in raw.decode("utf-8", "replace").split("\n\n"):
        name, payload = None, None
        for line in block.splitlines():
            if line.startswith("event: "):
                name = line[len("event: "):]
            elif line.startswith("data: "):
                payload = line[len("data: "):]
        if name or payload:
            events.append((name, payload))
    return events


print("== T1 Gemini upstream × five entries")

status, _, body = http_json(
    "POST", "/v1/images/generations",
    {"model": "xx", "prompt": "a cat", "response_format": "b64_json"},
)
check("T1-1 images/generations 200", status == 200, f"status={status} body={body}")
items = (body.get("data") or [{}]) if isinstance(body, dict) else [{}]
check("T1-1 images/generations b64_json", bool(items[0].get("b64_json")), str(body)[:200])
upstream = mock_last()
parts = (((upstream.get("body") or {}).get("contents") or [{}])[0].get("parts") or [])
check("T1-1 上游走 :generateContent", upstream.get("path", "").endswith(":generateContent"), upstream.get("path"))
check("T1-1 上游 text-only", len(parts) == 1 and "text" in parts[0], parts)
# 只声明 IMAGE：唯一能压住「草稿 + 成品」两张的开关（candidateCount / thinkingBudget 无效）。
check(
    "T1-1 上游只声明 IMAGE 模态",
    ((upstream.get("body") or {}).get("generationConfig") or {}).get("responseModalities") == ["IMAGE"],
    (upstream.get("body") or {}).get("generationConfig"),
)

raw, ctype = multipart(
    {"model": "xx", "prompt": "make it watercolor"},
    {"image": ("cat.png", PNG, "image/png")},
)
status, _, body = http("POST", "/v1/images/edits", raw, ctype=ctype)
parsed = json.loads(body or b"{}")
check("T1-2 images/edits 200", status == 200, f"status={status} body={body[:200]}")
items = (parsed.get("data") or [{}]) if isinstance(parsed, dict) else [{}]
check("T1-2 images/edits b64_json", bool(items[0].get("b64_json")), str(parsed)[:200])
upstream = mock_last()
parts = (((upstream.get("body") or {}).get("contents") or [{}])[0].get("parts") or [])
check("T1-2 上游含 inlineData", any("inlineData" in p for p in parts), parts)

status, _, body = http_json(
    "POST", "/v1/chat/completions",
    {"model": "xx", "messages": [{"role": "user", "content": "a cat"}]},
)
content = (((body.get("choices") or [{}])[0].get("message") or {}).get("content") or "")
check("T1-3 chat 非流式 200", status == 200, f"status={status} body={str(body)[:200]}")
check("T1-3 chat content 是 markdown", "![image](data:image/png;base64," in content, content[:120])

status, _, body = http_json(
    "POST", "/v1/responses",
    {"model": "xx", "input": "a cat"},
)
output = (body.get("output") or [{}])[0]
out_content = (output.get("content") or [{}])[0]
check("T1-4 responses 非流式 200", status == 200, f"status={status} body={str(body)[:200]}")
check("T1-4 output_text", out_content.get("type") == "output_text", out_content)
check("T1-4 text 是 markdown", "![image](data:image/png;base64," in (out_content.get("text") or ""), "")

status, _, body = http_json(
    "POST", "/v1/responses",
    {"model": "xx", "input": "a cat", "stream": True},
)
text = body.get("_raw", "")
events = [name for name, _ in sse_events(text.encode())]
check("T1-5 responses 流式 200", status == 200, f"status={status} body={text[:200]}")
check(
    "T1-5 事件序列",
    events == [
        "response.created",
        "response.output_item.added",
        "response.output_text.delta",
        "response.output_text.done",
        "response.output_item.done",
        "response.completed",
    ],
    events,
)
check("T1-5 delta 含 markdown", "![image](data:image/png;base64," in text, "")
check("T1-5 completed 带 usage", '"total_tokens":1297' in text, text[-300:])

status, _, body = http_json(
    "POST", "/v1beta/models/xx:generateContent",
    {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]},
)
candidate = ((body.get("candidates") or [{}])[0]) if isinstance(body, dict) else {}
inline = (((candidate.get("content") or {}).get("parts") or [{}])[0].get("inlineData") or {})
check("T1-6 v1beta 非流式 200", status == 200, f"status={status} body={str(body)[:200]}")
check("T1-6 inlineData", inline.get("mimeType") == "image/png" and bool(inline.get("data")), inline)
check("T1-6 finishReason/index", candidate.get("finishReason") == "STOP" and candidate.get("index") == 0, candidate)
check("T1-6 modelVersion 用请求名", body.get("modelVersion") == "xx", body.get("modelVersion"))

status, _, body = http_json(
    "POST", "/v1beta/models/xx:streamGenerateContent?alt=sse",
    {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]},
)
raw_text = body.get("_raw", "") if isinstance(body, dict) else ""
check("T1-7 alt=sse 200", status == 200, f"status={status}")
check("T1-7 是 SSE", raw_text.startswith("data: ") and "inlineData" in raw_text, raw_text[:120])

status, _, body = http_json(
    "POST", "/v1beta/models/xx:streamGenerateContent",
    {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]},
)
check("T1-8 无 alt 回 JSON 数组", status == 200 and isinstance(body, list), str(body)[:160])

before = mock_last()
status, _, body = http_json(
    "POST", "/v1/images/generations",
    {"model": "xx", "prompt": "two cats", "n": 2},
)
check("T1-9 n=2 拒绝", status == 400, f"status={status} body={str(body)[:200]}")
check("T1-9 n=2 未发上游", mock_last() == before, "upstream received a request")

print("== T2 non-Gemini upstream × Gemini entry")

for model, label in [("yy", "ark"), ("zz", "openai")]:
    status, _, body = http_json(
        "POST", f"/v1beta/models/{model}:generateContent",
        {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]},
    )
    candidate = ((body.get("candidates") or [{}])[0]) if isinstance(body, dict) else {}
    inline = (((candidate.get("content") or {}).get("parts") or [{}])[0].get("inlineData") or {})
    check(f"T2-{label} 200", status == 200, f"status={status} body={str(body)[:200]}")
    check(f"T2-{label} data[] 重建为 inlineData", inline.get("mimeType") == "image/png", inline)
    check(f"T2-{label} modelVersion 不暴露上游", body.get("modelVersion") == model, body.get("modelVersion"))

print("== T3 indistinguishability")

shapes = []
headers_seen = []
for model in ["xx", "yy", "zz"]:
    status, headers, body = http_json(
        "POST", f"/v1beta/models/{model}:generateContent",
        {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]},
    )
    candidate = ((body.get("candidates") or [{}])[0]) if isinstance(body, dict) else {}
    content = (candidate.get("content") or {})
    parts = (content.get("parts") or [{}])[0]
    shapes.append((
        status,
        tuple(sorted(body.keys())),
        tuple(sorted(candidate.keys())),
        tuple(sorted(content.keys())),
        tuple(sorted(parts.keys())),
        candidate.get("finishReason"),
        candidate.get("index"),
    ))
    headers_seen.append({k.lower() for k in headers})

check("T3 三个上游回包字段集合相同", len({json.dumps(s, default=str) for s in shapes}) == 1, shapes)
for leaked in ["server", "x-goog-request-id", "set-cookie"]:
    check(f"T3 响应头不含 {leaked}", all(leaked not in h for h in headers_seen), [sorted(h) for h in headers_seen])

status, _, body = http_json(
    "POST", "/v1beta/models/nope:generateContent",
    {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]},
)
error = body.get("error") if isinstance(body, dict) else None
check(
    "T3 未知模型 503 model_not_found",
    status == 503 and (error or {}).get("code") == "model_not_found",
    f"status={status} body={str(body)[:200]}",
)

# T1-6：上游一次回两张（nano-banana-2.1 的草稿 + 成品）。OpenAI 形状的入口
# 只交付最后一张，Gemini 原生入口按 Gemini 语义保留两个 part。
status, _, body = http_json("POST", "/v1/images/generations",
                             {"model": "xt", "prompt": "a cat", "response_format": "b64_json"})
items = (body or {}).get("data") or []
check("T1-6 双图上游 images 只交付 1 张", status == 200 and len(items) == 1,
      f"status={status} len={len(items)}")

status, _, body = http_json("POST", "/v1beta/models/xt:generateContent",
                             {"contents": [{"role": "user", "parts": [{"text": "a cat"}]}]})
parts = (((body or {}).get("candidates") or [{}])[0].get("content") or {}).get("parts") or []
inline = [p for p in parts if "inlineData" in p]
check("T1-6 双图上游 Gemini 入口保留 2 张", status == 200 and len(inline) == 2,
      f"status={status} inline={len(inline)}")
usage = (body or {}).get("usageMetadata") or {}
check("T1-6 usageMetadata 用上游真实 token",
      usage.get("promptTokenCount") == 7 and usage.get("totalTokenCount") == 1297,
      usage)
check("T1-6 usageMetadata 不含 trafficType", "trafficType" not in usage, usage)

# T1-10：按 token 计价的模型（xr 无 ModelPrice，ModelRatio=CompletionRatio=1）。
# 响应里的 usage 与账本同源，所以用响应断言即可；账本已实测 1297。
status, _, body = http_json("POST", "/v1/chat/completions", {
    "model": "xr", "messages": [{"role": "user", "content": "a cat"}],
})
usage = (body or {}).get("usage") or {}
check("T1-10 按 token 计价的模型拿到真实用量",
      status == 200 and usage.get("prompt_tokens") == 7
      and usage.get("completion_tokens") == 1290
      and usage.get("total_tokens") == 1297,
      f"status={status} usage={usage}")

# 对照：按次计价的模型（xx 有 ModelPrice）不能被 token 用量影响。
status, _, body = http_json("POST", "/v1/chat/completions", {
    "model": "xx", "messages": [{"role": "user", "content": "a cat"}],
})
check("T1-10 按次计价的模型仍然 200", status == 200, f"status={status}")

print()
if failed:
    print(f"FAILED {len(failed)}/{ok + len(failed)}: {', '.join(failed)}")
    sys.exit(1)
print(f"OK {ok} checks")
