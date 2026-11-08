import json, urllib.request, urllib.error, time

BASE = 'http://127.0.0.1:3901'

def call(method, path, body=None, token=None, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header('Content-Type', 'application/json')
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            payload = r.read().decode()
            return r.status, (payload if raw else json.loads(payload or '{}'))
    except urllib.error.HTTPError as e:
        payload = e.read().decode()
        return e.code, (payload if raw else json.loads(payload or '{}'))

# 1) setup
code, body = call('POST', '/api/setup', {
    'username': 'root', 'password': 'Apiduo12345', 'confirmPassword': 'Apiduo12345',
    'SelfUseModeEnabled': True, 'DemoSiteEnabled': False,
})
print('setup', code, body.get('success'))

# 2) 登录
code, body = call('POST', '/api/user/login', {'username': 'root', 'password': 'Apiduo12345'})
token = body['data']['access_token']
print('login', code, 'token-len', len(token))

# 3) 渠道（type=1 OpenAI 兼容，指向 mock）
code, body = call('POST', '/api/channel/', {'mode': 'single', 'channel': {
    'name': 'e2e-mock', 'type': 1, 'key': 'sk-mock',
    'base_url': 'http://127.0.0.1:3911',
    'models': 'gpt-4o,gpt-4o-mini,gpt-image-1,dall-e-3,sora-2',
    'group': 'default', 'status': 1}}, token)
print('channel', code, body.get('success'))

# 4) 关性能监控，避免磁盘使用率拦截
call('PUT', '/api/option/', {'key': 'performance_setting.monitor_enabled', 'value': 'false'}, token)
call('PUT', '/api/option/', {'key': 'performance_setting.monitor_disk_threshold', 'value': '100'}, token)

# 5) 建一个无限额度的 API 令牌（给 /v1 用）
code, body = call('POST', '/api/token/', {
    'name': 'e2e-token', 'remain_quota': 0, 'unlimited_quota': True,
    'expired_time': -1, 'model_limits_enabled': False, 'group': ''}, token)
tid = body.get('data', {}).get('id')
if tid:
    code, body = call('POST', f'/api/token/{tid}/key', None, token)
    sk = body['data']['key']
else:
    sk = ''
print('api token', 'id', tid, 'sk-len', len(sk))

json.dump({'token': token, 'sk': sk, 'base': BASE}, open('/tmp/apiduo-e2e/env.json', 'w'))
print('env saved')
