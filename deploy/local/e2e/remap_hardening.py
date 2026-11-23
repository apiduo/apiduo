#!/usr/bin/env python3
"""End-to-end regression for the channel remap-hardening switch.

Prerequisites (all local, no spend):

    ./up.sh --mock                     # MySQL + ClickHouse + app + mock upstream
    # a channel named mock-remap with type 1, base_url http://mock-upstream:18080
    # and the model_mapping the setup step below writes

Environment:

    APIDUO_BASE      gateway base url           (default http://127.0.0.1:39047)
    APIDUO_TOKEN     an API token (sk-...)      (required)
    APIDUO_ADMIN     dashboard user for setup   (default root)
    APIDUO_PASSWORD  its password               (required for setup/control cases)
    APIDUO_CHANNEL   channel id                 (default 251)

Without APIDUO_PASSWORD the suite still runs the client-facing cases; it skips
the unhарdened control path, which needs to flip the channel settings.

Exit 0 means every executed assertion passed.
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

BASE = os.environ.get('APIDUO_BASE', 'http://127.0.0.1:39047')
TOKEN = os.environ.get('APIDUO_TOKEN', '')
ADMIN = os.environ.get('APIDUO_ADMIN', 'root')
PASSWORD = os.environ.get('APIDUO_PASSWORD', '')
CHANNEL_ID = int(os.environ.get('APIDUO_CHANNEL', '251'))

MODELS = {
    'acme-pro': 'deepseek-v3-250101',
    'acme-err400': 'fail-400',
    'acme-err404': 'fail-404',
    'acme-err429': 'fail-429',
    'acme-err500': 'fail-500',
    'acme-ctx': 'fail-ctx',
    'acme-filter': 'fail-filter',
    'acme-auth': 'fail-auth',
    'acme-perm': 'fail-perm',
    'acme-large': 'fail-large',
    'acme-param': 'fail-param',
    'acme-503': 'fail-503',
    'acme-nonjson': 'fail-nonjson',
    'acme-empty': 'fail-empty',
}

ERRORS = [
    ('acme-err400', 400, 'invalid_request', 'invalid_request_error'),
    ('acme-err404', 404, 'model_not_found', 'not_found_error'),
    ('acme-err429', 429, 'rate_limit_exceeded', 'rate_limit_error'),
    ('acme-err500', 502, 'upstream_unavailable', 'server_error'),
    ('acme-ctx', 400, 'context_length_exceeded', 'invalid_request_error'),
    ('acme-filter', 400, 'content_filter', 'invalid_request_error'),
    ('acme-auth', 401, 'invalid_api_key', 'authentication_error'),
    ('acme-perm', 403, 'model_not_authorized', 'permission_error'),
    ('acme-large', 413, 'request_too_large', 'invalid_request_error'),
    ('acme-param', 400, 'unsupported_parameter', 'invalid_request_error'),
    ('acme-503', 502, 'upstream_unavailable', 'server_error'),
    ('acme-nonjson', 502, 'upstream_unavailable', 'server_error'),
    ('acme-empty', 502, 'upstream_unavailable', 'server_error'),
]

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
RESULTS = []


def record(name, ok, detail=''):
    RESULTS.append((name, ok, detail))
    print(('PASS  ' if ok else 'FAIL  ') + name + ('' if ok else '  -> ' + str(detail)[:180]))


def http(method, path, payload=None, token=None, uid=None, headers=None, raw_body=None):
    body = raw_body if raw_body is not None else (
        json.dumps(payload).encode() if payload is not None else None)
    req = urllib.request.Request(BASE + path, data=body, method=method)
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    if uid is not None:
        req.add_header('New-Api-User', str(uid))
    for key, value in (headers or {}).items():
        req.add_header(key, value)
    if body is not None and (headers or {}).get('Content-Type') is None:
        req.add_header('Content-Type', 'application/json')
    try:
        with OPENER.open(req, timeout=90) as resp:
            return resp.status, dict(resp.headers), resp.read().decode()
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers), exc.read().decode()


def admin_token():
    if not PASSWORD:
        return None
    status, _, body = http('POST', '/api/user/login',
                           {'username': ADMIN, 'password': PASSWORD})
    if status != 200:
        return None
    return json.loads(body)['data']['access_token']


def chat(model, stream=False, token=None):
    return http('POST', '/v1/chat/completions',
                {'model': model, 'stream': stream,
                 'messages': [{'role': 'user', 'content': 'remap e2e'}]},
                token=token or TOKEN)


def claude(model, stream=False, token=None):
    return http('POST', '/v1/messages',
                {'model': model, 'max_tokens': 64, 'stream': stream,
                 'messages': [{'role': 'user', 'content': 'remap e2e'}]},
                token=token or TOKEN, headers={'anthropic-version': '2023-06-01'})


def gemini(model, token=None):
    return http('POST', f'/v1beta/models/{model}:generateContent',
                {'contents': [{'parts': [{'text': 'remap e2e'}]}]},
                token=token or TOKEN)


def responses(model, token=None):
    return http('POST', '/v1/responses', {'model': model, 'input': 'remap e2e'},
                token=token or TOKEN)


def sse_payloads(body):
    out = []
    for line in body.split('\n'):
        if not line.startswith('data:'):
            continue
        value = line[5:].strip()
        if not value or value == '[DONE]':
            continue
        try:
            out.append(json.loads(value))
        except json.JSONDecodeError:
            pass
    return out


def leak_free(body):
    lowered = body.lower()
    return 'fail-' not in lowered and 'deepseek' not in lowered


def setup_channel(jwt):
    """Idempotent: make the channel serve exactly the models this suite needs."""
    payload = {
        'id': CHANNEL_ID,
        'models': ','.join(MODELS),
        'model_mapping': json.dumps(MODELS, separators=(',', ':')),
        'settings': json.dumps({'remap_hardening': True}),
    }
    status, _, body = http('PUT', '/api/channel/', payload, token=jwt, uid=1)
    return status == 200 and json.loads(body).get('success') is True


def set_switch(jwt, on):
    http('PUT', '/api/channel/', {'id': CHANNEL_ID,
                                  'settings': json.dumps({'remap_hardening': on})},
         token=jwt, uid=1)


# --------------------------------------------------------------- protocols
def suite_protocols():
    status, _, body = chat('acme-pro')
    obj = json.loads(body)
    record('openai chat: 200', status == 200, status)
    record('openai chat: model is the requested name', obj.get('model') == 'acme-pro', obj.get('model'))
    record('openai chat: fingerprint nulled', obj.get('system_fingerprint') is None)
    record('openai chat: no upstream name', leak_free(body))

    status, headers, body = chat('acme-pro', stream=True)
    chunks = sse_payloads(body)
    record('openai stream: 200 + sse',
           status == 200 and 'text/event-stream' in headers.get('Content-Type', ''))
    record('openai stream: chunks present', len(chunks) >= 4, len(chunks))
    record('openai stream: every model rewritten',
           all(c.get('model') == 'acme-pro' for c in chunks), {c.get('model') for c in chunks})
    record('openai stream: fingerprints nulled',
           all(c.get('system_fingerprint') is None for c in chunks))
    record('openai stream: DONE kept', 'data: [DONE]' in body)

    status, _, body = claude('acme-pro')
    payload = json.loads(body) if body.strip().startswith('{') else {}
    record('claude: 200', status == 200, status)
    record('claude: model rewritten', payload.get('model') == 'acme-pro', payload.get('model'))
    record('claude: no upstream name', leak_free(body))

    status, headers, body = claude('acme-pro', stream=True)
    record('claude stream: 200 + sse',
           status == 200 and 'text/event-stream' in headers.get('Content-Type', ''), status)

    status, _, body = gemini('acme-pro')
    record('gemini: 200', status == 200, status)
    record('gemini: no upstream name', leak_free(body))

    status, _, body = responses('acme-pro')
    record('responses: 200', status == 200, status)
    record('responses: no upstream name', leak_free(body))


# --------------------------------------------------------------- error table
def suite_errors():
    for model, want_status, want_code, want_type in ERRORS:
        status, headers, body = chat(model)
        try:
            err = json.loads(body).get('error') or {}
        except json.JSONDecodeError:
            err = {}
        record(f'{model}: HTTP {want_status}', status == want_status, status)
        record(f'{model}: code {want_code}', err.get('code') == want_code, err.get('code'))
        record(f'{model}: type {want_type}', err.get('type') == want_type, err.get('type'))
        record(f'{model}: leak-free body', leak_free(body), body[:100])
        if model == 'acme-err429':
            record('acme-err429: Retry-After kept', headers.get('Retry-After') == '12',
                   headers.get('Retry-After'))

    status, _, body = claude('acme-err404')
    payload = json.loads(body) if body.strip().startswith('{') else {}
    record('claude error: HTTP 404', status == 404, status)
    record('claude error: envelope type=error', payload.get('type') == 'error', payload.get('type'))
    inner = payload.get('error') or {}
    record('claude error: inner type not_found_error',
           inner.get('type') == 'not_found_error', inner.get('type'))
    record('claude error: no upstream name', leak_free(body))

    status, _, body = gemini('acme-err404')
    record('gemini error: HTTP 404', status == 404, status)
    record('gemini error: body has error object', 'error' in body, body[:100])
    record('gemini error: no upstream name', leak_free(body))


# --------------------------------------------------------------- cleanliness
# Values the model itself produced. A model may name itself; that is content,
# not a gateway identifier, and rewriting it would corrupt the answer.
CONTENT_KEYS = {'content', 'text', 'reasoning_content', 'arguments', 'input', 'message'}
LEAK = re.compile(r'deepseek|fail-|oai_chat|openai_usage|usage_semantic|usage_source|anthropic|gemini|vendor', re.I)

# Response headers that name the vendor or date the build. None of them is
# needed to use the API.
FORBIDDEN_HEADERS = (
    'server',
    'openai-organization',
    'openai-processing-ms',
    'openai-version',
    'anthropic-organization-id',
    'x-request-id',
    'set-cookie',
    'x-new-api-version',
    'x-ratelimit-limit-requests',
    'x-ratelimit-remaining-requests',
)


def scan_leaks(node, path, hits):
    if isinstance(node, dict):
        for key, value in node.items():
            if key in CONTENT_KEYS:
                continue
            if LEAK.search(key):
                hits.append(f'{path}.{key} (key name)')
            scan_leaks(value, f'{path}.{key}', hits)
    elif isinstance(node, list):
        for i, value in enumerate(node):
            scan_leaks(value, f'{path}[{i}]', hits)
    elif isinstance(node, str) and LEAK.search(node):
        hits.append(f'{path} = {node[:80]}')


def suite_cleanliness():
    cases = [
        ('openai chat', '/v1/chat/completions',
         {'model': 'acme-pro', 'messages': [{'role': 'user', 'content': 'clean'}]}, None),
        ('claude', '/v1/messages',
         {'model': 'acme-pro', 'max_tokens': 32,
          'messages': [{'role': 'user', 'content': 'clean'}]},
         {'anthropic-version': '2023-06-01'}),
        ('gemini', '/v1beta/models/acme-pro:generateContent',
         {'contents': [{'parts': [{'text': 'clean'}]}]}, None),
        ('responses', '/v1/responses', {'model': 'acme-pro', 'input': 'clean'}, None),
    ]
    for label, path, payload, headers in cases:
        status, resp_headers, body = http('POST', path, payload, token=TOKEN, headers=headers)
        if status != 200:
            record(f'clean {label}: reachable', False, status)
            continue
        hits = []
        try:
            scan_leaks(json.loads(body), '$', hits)
        except json.JSONDecodeError:
            hits.append('body is not JSON')
        header_hits = [f'{k}: {v}' for k, v in resp_headers.items()
                       if LEAK.search(k) or LEAK.search(v or '')]
        record(f'clean {label}: metadata leak-free', not hits and not header_hits,
               hits + header_hits)
        lowered = {key.lower() for key in resp_headers}
        leaked = [key for key in FORBIDDEN_HEADERS if key in lowered]
        record(f'clean {label}: no vendor/build headers', not leaked, leaked)

    status, stream_headers, body = chat('acme-pro', stream=True)
    lowered = {key.lower() for key in stream_headers}
    leaked = [key for key in FORBIDDEN_HEADERS if key in lowered]
    record('clean openai stream: no vendor/build headers', not leaked, leaked)
    hits = []
    events = 0
    for line in body.split('\n'):
        line = line.strip()
        if not line:
            continue
        if not line.startswith('data:'):
            if LEAK.search(line):
                hits.append(f'sse line: {line[:80]}')
            continue
        value = line[5:].strip()
        if value == '[DONE]':
            continue
        try:
            scan_leaks(json.loads(value), f'event{events}', hits)
            events += 1
        except json.JSONDecodeError:
            if LEAK.search(value):
                hits.append(f'event{events}: {value[:80]}')
    record(f'clean openai stream: {events} events leak-free', not hits and events > 0, hits)


# --------------------------------------------------------------- control
def suite_control(jwt):
    set_switch(jwt, False)
    try:
        status, _, body = chat('acme-pro')
        obj = json.loads(body)
        record('control: upstream model visible',
               obj.get('model') == 'deepseek-v3-250101', obj.get('model'))
        record('control: fingerprint visible',
               obj.get('system_fingerprint') == 'fp_mock_123')

        status, _, body = chat('acme-err404')
        obj = json.loads(body)
        record('control: HTTP 404 unchanged', status == 404, status)
        record('control: upstream text preserved', 'fail-404' in json.dumps(obj), body[:120])
    finally:
        set_switch(jwt, True)


# --------------------------------------------------------------- internal
def suite_internal():
    status, _, body = chat('no-such-model-anywhere')
    record('unknown model: 4xx/5xx', status >= 400, status)
    record('unknown model: leak-free', leak_free(body))

    status, _, body = chat('acme-pro', token='sk-definitely-invalid')
    record('invalid token: 401', status == 401, status)

    status, _, body = http('POST', '/v1/chat/completions',
                           {'model': 'acme-pro', 'messages': []}, token=TOKEN)
    record('empty messages: 400', status == 400, status)

    status, _, body = http('POST', '/v1/chat/completions', raw_body=b'{bad json',
                           token=TOKEN, headers={'Content-Type': 'application/json'})
    record('malformed body: 4xx', 400 <= status < 500, status)


# --------------------------------------------------------------- concurrency
def suite_concurrency():
    def one(_):
        status, _, body = chat('acme-pro')
        try:
            return status, json.loads(body).get('model')
        except json.JSONDecodeError:
            return status, None

    with ThreadPoolExecutor(max_workers=10) as pool:
        rows = list(pool.map(one, range(20)))
    record('concurrency: all 200', all(s == 200 for s, _ in rows), {s for s, _ in rows})
    record('concurrency: every model rewritten',
           all(m == 'acme-pro' for _, m in rows), {m for _, m in rows})


# --------------------------------------------------------------- billing
def suite_billing(jwt):
    # Batch quota updates delay the user row, so billing is asserted on the log.
    chat('acme-pro')
    for _ in range(10):
        _, _, body = http('GET', '/api/log/?p=0&page_size=3&model_name=acme-pro&type=2',
                          token=jwt, uid=1)
        items = json.loads(body)['data']['items']
        if items and (items[0].get('quota') or 0) > 0:
            record('billing: settled quota > 0', True, items[0]['quota'])
            record('billing: usage tokens recorded',
                   (items[0].get('prompt_tokens') or 0) > 0 and
                   (items[0].get('completion_tokens') or 0) > 0,
                   (items[0].get('prompt_tokens'), items[0].get('completion_tokens')))
            return
        time.sleep(1.5)
    record('billing: settled quota > 0', False, 'no settled row within 15s')


def main():
    if not TOKEN:
        print('APIDUO_TOKEN is required (an sk-... API token)')
        return 2

    jwt = admin_token()
    if jwt:
        record('setup: channel serves the test models', setup_channel(jwt))
    else:
        print('note: APIDUO_PASSWORD absent; skipping setup and control cases\n')

    suite_protocols()
    suite_errors()
    suite_cleanliness()
    if jwt:
        suite_control(jwt)
    suite_internal()
    suite_concurrency()
    if jwt:
        suite_billing(jwt)

    failed = [r for r in RESULTS if not r[1]]
    print()
    print(f'{len(RESULTS) - len(failed)}/{len(RESULTS)} passed')
    if failed:
        print('FAILED:')
        for name, _, detail in failed:
            print('  -', name, '->', detail)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
