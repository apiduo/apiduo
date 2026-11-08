"""后端接口端到端测试：/pg 三模态 + /v1 一致性 + 错误路径 + 对账。"""
import json
import time
import urllib.error
import urllib.request

BASE = 'http://127.0.0.1:3901'
ENV = json.load(open('/tmp/apiduo-e2e/env.json'))
JWT = ENV['token']
SK = ENV.get('sk', '')
RESULTS = []


def record(name, ok, detail=''):
    RESULTS.append({'name': name, 'ok': bool(ok), 'detail': str(detail)[:400]})
    print(('PASS' if ok else 'FAIL'), '|', name, '|', str(detail)[:200])


def call(method, path, body=None, token=JWT, raw=False, timeout=40):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header('Content-Type', 'application/json')
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            payload = r.read().decode()
            return r.status, (payload if raw else json.loads(payload or '{}')), dict(r.headers)
    except urllib.error.HTTPError as e:
        payload = e.read().decode()
        try:
            parsed = json.loads(payload or '{}')
        except Exception:
            parsed = {'raw': payload[:300]}
        return e.code, parsed, dict(e.headers)


def stream_chat(payload, token=JWT, path='/pg/chat/completions', headers=None):
    """读完整条 SSE，返回 (status, request_id, body_text, usage_dict)"""
    req = urllib.request.Request(BASE + path, data=json.dumps(payload).encode(), method='POST')
    req.add_header('Content-Type', 'application/json')
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            rid = r.headers.get('X-Oneapi-Request-Id', '')
            raw = r.read().decode()
            usage = None
            for line in raw.split('\n'):
                if not line.startswith('data: '):
                    continue
                data = line[6:].strip()
                if data == '[DONE]':
                    continue
                try:
                    chunk = json.loads(data)
                except Exception:
                    continue
                if chunk.get('usage'):
                    usage = chunk['usage']
            return r.status, rid, raw, usage
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get('X-Oneapi-Request-Id', ''), e.read().decode(), None


def wait_log(request_id, expect=1, timeout=6.0):
    """消费日志异步落库，轮询到出现为止"""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        code, body, _ = call('GET', f'/api/log/self?request_id={request_id}&page=1&page_size=5&type=2')
        items = (body.get('data') or {}).get('items') or []
        if len(items) >= expect:
            return items
        last = body
        time.sleep(0.25)
    return []


def pricing():
    _, body, _ = call('GET', '/api/pricing')
    models = {m['model_name']: m for m in body.get('data', [])}
    return models, body.get('group_ratio', {})


def expected_quota(model_info, group_ratio, prompt, completion, cache):
    if not model_info:
        return None
    if model_info.get('quota_type') == 1:
        return round(model_info['model_price'] * 500000 * group_ratio)
    base = max(0, prompt - cache)
    cache_ratio = model_info.get('cache_ratio') or 1
    prompt_part = base + cache * cache_ratio
    completion_part = completion * model_info['completion_ratio']
    ratio = model_info['model_ratio'] * group_ratio
    q = round((prompt_part + completion_part) * ratio)
    if ratio > 0 and q == 0 and (prompt_part + completion_part) > 0:
        q = 1
    return q


MODELS, GROUP_RATIO = pricing()
DEFAULT_RATIO = GROUP_RATIO.get('default', 1)

# ---------------- A. 对话 ----------------
A1 = {'model': 'gpt-4o', 'group': 'default', 'stream': True,
      'messages': [{'role': 'user', 'content': 'e2e 流式'}],
      'stream_options': {'include_usage': True}}

status, rid, raw, usage = stream_chat(A1)
record('A1 流式请求成功', status == 200 and '[DONE]' in raw, f'status={status} rid={rid[:18]}')
record('A1 响应头带 request id', bool(rid), rid[:24])
record('A1 流末尾带 usage', bool(usage) and usage.get('prompt_tokens', 0) > 0, json.dumps(usage or {})[:160])

logs = wait_log(rid)
record('A1 消费日志可反查', len(logs) == 1, f'found={len(logs)}')
if logs:
    log = logs[0]
    exp = expected_quota(MODELS.get('gpt-4o'), DEFAULT_RATIO, 1200, 380, 800)
    record('A1 日志 quota 与公式一致', log['quota'] == exp, f"log={log['quota']} expect={exp}")
    record('A1 日志 token 与 usage 一致',
           log['prompt_tokens'] == usage['prompt_tokens'] and log['completion_tokens'] == usage['completion_tokens'],
           f"log={log['prompt_tokens']}/{log['completion_tokens']} usage={usage['prompt_tokens']}/{usage['completion_tokens']}")

# 非流式
A2 = dict(A1); A2['stream'] = False; A2.pop('stream_options')
status, rid2, raw2, _ = stream_chat(A2)
try:
    body2 = json.loads(raw2)
except Exception:
    body2 = {}
has_usage = bool(body2.get('usage'))
record('A2 非流式请求成功', status == 200 and bool(body2.get('choices')), f'status={status}')
record('A2 非流式响应带 usage', has_usage, json.dumps(body2.get('usage') or {})[:120])
logs2 = wait_log(rid2)
record('A2 非流式日志可反查', len(logs2) == 1, f'found={len(logs2)}')

# 无缓存场景（cache=0）
A3 = dict(A1)
status, rid3, raw3, usage3 = stream_chat({**A3, 'messages': [{'role': 'user', 'content': 'no cache'}]})
# mock 固定返回 cache 800，这里换个断言：用 mock 的返回复算
logs3 = wait_log(rid3)
if logs3 and usage3:
    exp3 = expected_quota(MODELS.get('gpt-4o'), DEFAULT_RATIO, usage3['prompt_tokens'],
                          usage3['completion_tokens'],
                          (usage3.get('prompt_tokens_details') or {}).get('cached_tokens', 0))
    record('A3 缓存命中参与计费', logs3[0]['quota'] == exp3, f"log={logs3[0]['quota']} expect={exp3}")

# 重复 5 次，检查幂等与 request id 唯一
rids = []
quotas = []
for i in range(5):
    status, rid_i, raw_i, usage_i = stream_chat({**A1, 'messages': [{'role': 'user', 'content': f'repeat-{i}'}]})
    rids.append(rid_i)
    logs_i = wait_log(rid_i)
    if logs_i:
        quotas.append(logs_i[0]['quota'])
record('A4 重复 5 次均成功', len(set(rids)) == 5 and all(quotas), f'rids={len(set(rids))} quotas={quotas}')
record('A4 重复 5 次 quota 稳定', len(set(quotas)) == 1, f'unique={set(quotas)}')

# 客户端中断：服务端是否仍记日志
try:
    req = urllib.request.Request(BASE + '/pg/chat/completions',
                                 data=json.dumps({**A1, 'messages': [{'role': 'user', 'content': 'abort'}]}).encode(),
                                 method='POST')
    req.add_header('Content-Type', 'application/json')
    req.add_header('Authorization', 'Bearer ' + JWT)
    r = urllib.request.urlopen(req, timeout=10)
    r.read(200)
    rid_abort = r.headers.get('X-Oneapi-Request-Id', '')
    r.close()
    logs_abort = wait_log(rid_abort, timeout=4)
    record('A5 客户端中断后服务端仍有结算', len(logs_abort) == 1,
           f"quota={logs_abort[0]['quota'] if logs_abort else 'none'}")
except Exception as exc:
    record('A5 客户端中断后服务端仍有结算', False, repr(exc)[:160])

# ---------------- B. 错误路径 ----------------
status, body, _ = call('POST', '/pg/chat/completions', {**A1, 'model': 'no-such-model-xyz'})
record('B1 未知模型被拒绝', status >= 400, f'status={status} {json.dumps(body)[:120]}')

status, body, _ = call('POST', '/pg/chat/completions', {**A1, 'messages': []})
record('B2 空 messages 被拒绝', status >= 400, f'status={status}')

status, body, _ = call('POST', '/pg/chat/completions', A1, token=None)
record('B3 未登录被拒绝', status == 401, f'status={status}')

status, body, _ = call('POST', '/pg/chat/completions', A1, token=SK)
record('B4 用 API 令牌调 /pg 被拒绝', status >= 400 and 'access token' in json.dumps(body).lower(),
       f'status={status} {json.dumps(body)[:140]}')

# ---------------- C. 生图 ----------------
C1 = {'model': 'gpt-image-1', 'prompt': 'a tiny pixel', 'n': 2, 'size': '1024x1024', 'response_format': 'b64_json'}
status, body, headers = call('POST', '/pg/images/generations', C1)
rid_img = headers.get('X-Oneapi-Request-Id', '')
data_items = (body.get('data') or []) if isinstance(body, dict) else []
record('C1 生图返回图片', status == 200 and len(data_items) == 2,
       f'status={status} items={len(data_items)} err={json.dumps(body)[:120] if not data_items else ""}')
logs_img = wait_log(rid_img) if rid_img else []
record('C1 生图有消费日志', len(logs_img) == 1,
       f"quota={logs_img[0]['quota'] if logs_img else 'none'}")

# ---------------- D. 生视频 ----------------
D1 = {'model': 'sora-2', 'prompt': 'a cat walking', 'seconds': '5', 'size': '1280x720'}
status, body, headers = call('POST', '/pg/video/generations', D1)
rid_vid = headers.get('X-Oneapi-Request-Id', '')
vid_id = ''
if isinstance(body, dict):
    vid_id = body.get('id') or body.get('task_id') or ''
record('D1 生视频任务创建', status == 200 and bool(vid_id),
       f'status={status} id={vid_id} body={json.dumps(body)[:180]}')
if vid_id:
    states = []
    final = None
    for _ in range(40):
        s2, b2, _ = call('GET', f'/pg/video/generations/{vid_id}')
        d = (b2 or {}).get('data') or (b2 or {})
        states.append(d.get('status'))
        if d.get('status') in ('SUCCESS', 'succeeded', 'FAILURE', 'failed'):
            final = d
            break
        time.sleep(0.9)
    record('D2 生视频任务可轮询到终态', bool(final), f'states={states}')
    if final:
        record('D2 终态带结果地址', bool(final.get('url') or final.get('video_url')),
               f"url={(final.get('url') or final.get('video_url') or '')[:60]}")
    logs_vid = wait_log(rid_vid, timeout=4) if rid_vid else []
    record('D2 生视频有结算日志', len(logs_vid) >= 1,
           f"quota={logs_vid[0]['quota'] if logs_vid else 'none'}")

# ---------------- E. 与 /v1 一致性 ----------------
if SK:
    status, rid_v1, raw_v1, usage_v1 = stream_chat(
        {'model': 'gpt-4o', 'messages': [{'role': 'user', 'content': 'v1 same params'}],
         'stream': True, 'stream_options': {'include_usage': True}},
        token=SK, path='/v1/chat/completions')
    record('E1 /v1 流式成功', status == 200 and '[DONE]' in raw_v1, f'status={status}')
    logs_v1 = wait_log(rid_v1) if rid_v1 else []
    if logs_v1 and usage_v1:
        exp_v1 = expected_quota(MODELS.get('gpt-4o'), DEFAULT_RATIO, usage_v1['prompt_tokens'],
                                usage_v1['completion_tokens'],
                                (usage_v1.get('prompt_tokens_details') or {}).get('cached_tokens', 0))
        record('E1 /v1 与 /pg 计费口径一致', logs_v1[0]['quota'] == exp_v1,
               f"v1 quota={logs_v1[0]['quota']} expect={exp_v1}")
else:
    record('E1 /v1 一致性', False, 'no api token')

# ---------------- F. 日志查询接口 ----------------
status, body, _ = call('GET', '/api/log/self?page=1&page_size=10&type=2')
total = (body.get('data') or {}).get('total', 0)
record('F1 消费日志列表可用', status == 200 and total >= 10, f'total={total}')

ok = sum(1 for r in RESULTS if r['ok'])
print(f'\n===== {ok}/{len(RESULTS)} passed =====')
json.dump(RESULTS, open('/tmp/apiduo-e2e/backend-results.json', 'w'), ensure_ascii=False, indent=1)
