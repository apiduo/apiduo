#!/usr/bin/env python3
"""End-to-end check for expression pricing on a non-plugin task channel (8001).

Requirement: a model served by an advanced-async channel can be priced by an
expression that reads the task's own facts, and the settled quota follows it:

    per call    tier("request", u("count") * 0.10)
    per second  tier("base", u("seconds") * u("factor") * 0.01)
    per token   tier("base", u("tokens") * 10.0 / 1000000)

Two data points per mode, because one point cannot tell "priced by the
dimension" apart from "happened to equal a constant". The token count is
produced by the mock upstream from the requested duration, so it exists only
in the completion reply: a gateway that never reads it cannot invent 120000.

    python3 deploy/local/e2e/tiered_task_billing.py

Exit 0 means every assertion passed. Prints one PASS/FAIL line per criterion.
"""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
BASE = os.environ.get("APIDUO_BASE", "http://127.0.0.1:39047")
MYSQL = os.environ.get("APIDUO_MYSQL", "apiduo-local-mysql")
DB_NAME = os.environ.get("APIDUO_DB", "apiduo_local")
APP = os.environ.get("APIDUO_APP", "apiduo-local-app")
CH = os.environ.get("APIDUO_CH", "apiduo-local-clickhouse")
LOG_DB = os.environ.get("APIDUO_LOG_DB", "apiduo_local_logs")
ASYNC = "/v1/images/generations/async"

QUOTA_PER_UNIT = 500_000  # common.QuotaPerUnit
START_QUOTA = 5_000_000

ADMIN_USER = "tierbill-admin"
ADMIN_PASSWORD = "Passw0rd!2026"
BILL_USER = "tierbill-user"
BILL_USER_ID = 9001
BILL_TOKEN_ID = 9001
BILL_TOKEN_KEY = "tierbillingexpr000000000000000000000000000000"  # 客户端加 sk- 前缀

TOKENS_PER_SECOND = 24_000  # mock 的换算率，客户端请求里没有这个维度

def quota(amount_usd):
    """USD → quota, the same conversion the gateway applies at group ratio 1."""
    return int(round(amount_usd * QUOTA_PER_UNIT))


def token_count(seconds):
    return int(seconds * TOKENS_PER_SECOND)


PER_CALL = "tier-bill-call"
PER_SECOND = "tier-bill-second"
PER_TOKEN = "tier-bill-token"
LEGACY = "tier-bill-legacy"
# 渠道自己的 per_second（不是表达式）与「上游不报用量」的观察样本。
LEGACY_SECOND = "tier-bill-legacy-second"
NO_USAGE = "tier-bill-nousage"
NO_USAGE_CHANNEL = "tier-billing-nousage"

# 按 token 计价的标准形态：token > 次。上游不报用量时退到按次 —— 裸 token
# 表达式在这类回包下会结算 0。
#
# 不插「按秒」中间档是刻意的：秒档要读 u("seconds")，而该事实在请求没带
# duration 时不存在，写成 u("seconds") > 0 会直接 400 而不是退档；加上
# u("seconds") != nil 才安全，但那层保护是每个运营都要记得写的陷阱。
# 少一档换来的是「表达式不可能因为缺字段而拒绝请求」。
TOKEN_CHAIN = ('u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) '
               ': tier("call", u("count") * 0.20)')

EXPRESSIONS = {
    PER_CALL: 'tier("request", u("count") * 0.10)',
    PER_SECOND: 'tier("base", u("seconds") * u("factor") * 0.01)',
    PER_TOKEN: TOKEN_CHAIN,
    NO_USAGE: TOKEN_CHAIN,
}

CHANNEL_SETTINGS = {
    "advanced_async": {
        "protocol": "images",
        "incoming_paths": [],
        "submit": {"upstream_path": "/mock/tasks/submit", "method": "POST"},
        "query": {"upstream_path": "/mock/tasks/query/{task_id}", "method": "GET"},
        "fields": {
            "task_id_path": ["task_id"],
            "status_path": ["status"],
            "result_url_path": ["url"],
            "total_tokens_path": ["usage", "total_tokens"],
        },
        # 表达式不经过渠道的 legacy 模式；一个 factor 用来证明 u("factor")
        # 拿到的就是估算阶段用的那组倍率。models 里给一个模型单独指定
        # per_second，用作「老路径仍然按秒计价」的回归哨兵。
        "billing": {
            "mode": "per_call",
            "models": {LEGACY_SECOND: "per_second"},
            "factors": [{
                "key": "resolution",
                "path": ["resolution"],
                "values": {"480p": 1, "720p": 2},
                "default": 1,
            }],
        },
    }
}

# 第二个渠道：同样的提交端点，查询回包不带 usage。
NO_USAGE_SETTINGS = {
    "advanced_async": {
        "protocol": "images",
        "incoming_paths": [],
        "submit": {"upstream_path": "/mock/tasks/submit", "method": "POST"},
        "query": {"upstream_path": "/mock/tasks/query-nousage/{task_id}", "method": "GET"},
        "fields": {
            "task_id_path": ["task_id"],
            "status_path": ["status"],
            "result_url_path": ["url"],
            "total_tokens_path": ["usage", "total_tokens"],
        },
        "billing": {"mode": "per_call"},
    }
}

SETUP_SQL_TEMPLATE = """
DELETE FROM abilities WHERE model IN
  ('@@PER_CALL@@','@@PER_SECOND@@','@@PER_TOKEN@@','@@LEGACY@@','@@LEGACY_SECOND@@','@@NO_USAGE@@');
-- 早期版本把渠道的 models 串整行插进 abilities，留下一条逗号串的假模型。
DELETE FROM abilities WHERE model='@@MODELS@@';
DELETE FROM channels WHERE name IN ('tier-billing-async','@@NO_USAGE_CHANNEL@@');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (8001,'k',1,'tier-billing-async',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080',
        '@@MODELS@@','default',0,0,'@@SETTINGS@@');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (8001,'k',1,'@@NO_USAGE_CHANNEL@@',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080',
        '@@NO_USAGE@@','default',0,0,'@@NO_USAGE_SETTINGS@@');

-- abilities 是「一个模型一行」，不是把渠道的 models 串整行插进去：
-- 整串会变成一个叫 "a,b,c" 的模型，模型广场与定价列表都会认错。
INSERT INTO abilities (`group`,model,channel_id,enabled,priority,weight)
SELECT 'default', m.model, c.id, 1, c.priority, 1
FROM channels c
JOIN (
  SELECT 'tier-billing-async' AS channel_name, '@@PER_CALL@@' AS model
  UNION ALL SELECT 'tier-billing-async','@@PER_SECOND@@'
  UNION ALL SELECT 'tier-billing-async','@@PER_TOKEN@@'
  UNION ALL SELECT 'tier-billing-async','@@LEGACY@@'
  UNION ALL SELECT 'tier-billing-async','@@LEGACY_SECOND@@'
  UNION ALL SELECT '@@NO_USAGE_CHANNEL@@','@@NO_USAGE@@'
) m ON m.channel_name = c.name;

-- 专用管理员：密码哈希从 preview 复制（本地测试号，不引入新口令）。
INSERT INTO users (username,password,display_name,role,status,email,quota,used_quota,request_count,`group`,aff_code,created_at,auth_version)
SELECT '@@ADMIN@@', password, '@@ADMIN@@', 100, 1, '', 0, 0, 0, 'default', LEFT(MD5(RAND()),8), UNIX_TIMESTAMP(), 1
FROM users WHERE username='preview'
ON DUPLICATE KEY UPDATE password=VALUES(password), role=100, status=1;

-- 专用调用方：固定额度，余额才能证明扣费。
INSERT INTO users (id,username,password,display_name,role,status,email,quota,used_quota,request_count,`group`,aff_code,created_at,auth_version)
VALUES (@@USER_ID@@,'@@BILL_USER@@','','@@BILL_USER@@',1,1,'',@@START_QUOTA@@,0,0,'default','tierbill',UNIX_TIMESTAMP(),1)
ON DUPLICATE KEY UPDATE quota=@@START_QUOTA@@, used_quota=0, status=1, `group`='default';

INSERT INTO tokens (id,user_id,`key`,status,name,created_time,accessed_time,expired_time,remain_quota,unlimited_quota,model_limits_enabled,allow_ips,used_quota,`group`)
VALUES (@@TOKEN_ID@@,@@USER_ID@@,'@@TOKEN_KEY@@',1,'tier-billing',UNIX_TIMESTAMP(),UNIX_TIMESTAMP(),-1,@@START_QUOTA@@,0,0,'',0,'')
ON DUPLICATE KEY UPDATE remain_quota=@@START_QUOTA@@, used_quota=0, status=1, unlimited_quota=0;
"""


def setup_sql():
    sql = SETUP_SQL_TEMPLATE
    replacements = {
        "@@PER_CALL@@": PER_CALL,
        "@@PER_SECOND@@": PER_SECOND,
        "@@PER_TOKEN@@": PER_TOKEN,
        "@@LEGACY@@": LEGACY,
        "@@LEGACY_SECOND@@": LEGACY_SECOND,
        "@@NO_USAGE@@": NO_USAGE,
        "@@NO_USAGE_CHANNEL@@": NO_USAGE_CHANNEL,
        "@@MODELS@@": ",".join([PER_CALL, PER_SECOND, PER_TOKEN, LEGACY, LEGACY_SECOND]),
        "@@SETTINGS@@": json.dumps(CHANNEL_SETTINGS).replace("'", "''"),
        "@@NO_USAGE_SETTINGS@@": json.dumps(NO_USAGE_SETTINGS).replace("'", "''"),
        "@@ADMIN@@": ADMIN_USER,
        "@@BILL_USER@@": BILL_USER,
        "@@USER_ID@@": str(BILL_USER_ID),
        "@@TOKEN_ID@@": str(BILL_TOKEN_ID),
        "@@TOKEN_KEY@@": BILL_TOKEN_KEY,
        "@@START_QUOTA@@": str(START_QUOTA),
    }
    for token, value in replacements.items():
        sql = sql.replace(token, value)
    return sql


def mysql_password():
    return env_value("MYSQL_ROOT_PASSWORD")


def clickhouse_password():
    return env_value("CLICKHOUSE_PASSWORD")


def env_value(name):
    path = os.path.join(ROOT, "deploy", "local", ".env")
    with open(path, "r", encoding="utf-8") as handle:
        for line in handle:
            if line.startswith(name + "="):
                return line.split("=", 1)[1].strip()
    raise SystemExit(f"{name} not found in deploy/local/.env")


def db(sql, timeout=60):
    out = subprocess.run(
        ["docker", "exec", "-i", MYSQL, "mysql", "-uroot", f"-p{mysql_password()}",
         "-N", "--batch", DB_NAME],
        input=sql, capture_output=True, text=True, timeout=timeout,
    )
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip())
    return [line.split("\t") for line in out.stdout.splitlines() if line]


def ch(sql, timeout=60):
    """日志库是 ClickHouse（与生产同形态），不是 MySQL。"""
    out = subprocess.run(
        ["docker", "exec", CH, "clickhouse-client", f"--password={clickhouse_password()}",
         "-d", LOG_DB, "-q", sql],
        capture_output=True, text=True, timeout=timeout,
    )
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip())
    return [line.split("\t") for line in out.stdout.splitlines() if line]


def docker(*args, timeout=120):
    return subprocess.run(["docker", *args], capture_output=True, text=True, timeout=timeout)


def sql_str(value):
    return "'" + str(value).replace("'", "''") + "'"


def call(method, path, body=None, token=None, timeout=120):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(BASE + path, data=data, method=method)
    if token:
        request.add_header("Authorization", "Bearer " + token)
    if data is not None:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, json.loads(response.read() or b"{}")
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw or b"{}")
        except json.JSONDecodeError:
            return error.code, {"raw": raw.decode(errors="replace")}


def admin_login():
    # 会话有上限，残留会话会让登录直接 409；本地测试号只清自己的。
    db(f"DELETE FROM user_sessions WHERE user_id=(SELECT id FROM users WHERE username={sql_str(ADMIN_USER)});")
    status, body = call("POST", "/api/user/login", {"username": ADMIN_USER, "password": ADMIN_PASSWORD})
    token = ((body.get("data") or {}).get("access_token") or "")
    if status != 200 or not token:
        raise SystemExit(f"admin login failed: {status} {body}")
    return token


def wait_ready(timeout=120):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            status, body = call("GET", "/api/status", timeout=5)
            if status == 200 and body.get("success"):
                return True
        except Exception:
            pass
        time.sleep(2)
    return False


def restart_app():
    docker("restart", APP, timeout=120)
    return wait_ready()


def check(name, ok, detail=""):
    print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f"  [{detail}]" if detail else ""))
    return ok


def pricing_versions(admin, models):
    query = urllib.parse.urlencode([("model", model) for model in models])
    status, body = call("GET", "/api/option/model_pricing?" + query, token=admin)
    if status != 200 or not body.get("success"):
        raise SystemExit(f"model pricing snapshot failed: {status} {body}")
    data = body.get("data") or {}
    entries = {entry["model_name"]: entry for entry in data.get("entries") or []}
    return entries, data.get("empty_version", "")


def save_expressions(admin):
    """C1/C2: the save path an operator uses, same handler as the pricing UI."""
    models = list(EXPRESSIONS) + [LEGACY, LEGACY_SECOND]
    entries, empty_version = pricing_versions(admin, models)
    changes = []
    for model, expression in EXPRESSIONS.items():
        version = (entries.get(model) or {}).get("version") or empty_version
        changes.append({
            "model_name": model,
            "expected_version": version,
            "pricing": {
                "billing_setting.billing_mode": "tiered_expr",
                "billing_setting.billing_expr": expression,
            },
        })
    # 对照组：不写表达式，走渠道自己的 per_call / per_second + 模型固定价。
    for model in (LEGACY, LEGACY_SECOND):
        version = (entries.get(model) or {}).get("version") or empty_version
        changes.append({
            "model_name": model,
            "expected_version": version,
            "pricing": {"ModelPrice": 0.01},
        })
    status, body = call("PATCH", "/api/option/model_pricing", {"changes": changes}, token=admin)
    ok = check("save per-call / per-second / per-token expressions",
               status == 200 and body.get("success"), f"{status} {body.get('message', '')}")
    if not ok:
        return False

    # A key outside the built-in fact table must still be refused: this is the
    # guard that keeps a chat-only quantity from being priced as task usage.
    entries, empty_version = pricing_versions(admin, [PER_CALL])
    version = (entries.get(PER_CALL) or {}).get("version") or empty_version
    status, body = call("PATCH", "/api/option/model_pricing", {"changes": [{
        "model_name": PER_CALL,
        "expected_version": version,
        "pricing": {
            "billing_setting.billing_mode": "tiered_expr",
            "billing_setting.billing_expr": 'tier("bad", u("image_count") * 0.01)',
        },
    }]}, token=admin)
    message = str(body.get("message") or "")
    ok &= check("an unknown usage key is still rejected",
                status >= 400 and "usage" in message.lower(), f"{status} {message[:120]}")
    return ok


def submit(model, **fields):
    body = {"model": model, "prompt": "a red cube"}
    body.update(fields)
    return call("POST", ASYNC, body, token="sk-" + BILL_TOKEN_KEY)


def poll_client(task_id, timeout=180, interval=3):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        status, body = call("GET", f"{ASYNC}/{task_id}", token="sk-" + BILL_TOKEN_KEY, timeout=30)
        last = (status, body)
        if status == 200:
            return last
        time.sleep(interval)
    return last


def task_state(task_id):
    rows = db(f"SELECT status, quota FROM tasks WHERE task_id={sql_str(task_id)};")
    if not rows:
        return None
    return rows[0][0], int(rows[0][1])


def wait_settled(task_id, want, timeout=180, interval=2, settle_grace=20):
    """Status goes terminal before settlement, so wait on the quota itself.

    Once the task is terminal, a mismatching quota is a verdict, not a reason to
    keep waiting: settlement runs in the same call that writes the status. The
    grace window only covers the gap between those two writes, so a regression
    fails in seconds instead of burning the full timeout per case.
    """
    deadline = time.time() + timeout
    terminal_at = None
    last = None
    while time.time() < deadline:
        last = task_state(task_id)
        if last and last[0] == "FAILURE":
            return False, last
        if last and last[0] == "SUCCESS":
            if last[1] == want:
                return True, last
            terminal_at = terminal_at or time.time()
            if time.time() - terminal_at > settle_grace:
                return False, last
        time.sleep(interval)
    return False, last


def probe_missing_duration():
    """A per-second expression with no readable duration must fail loudly.

    Publishing seconds=0 instead would make the task free, and the client
    controls the body. The rejection must also name the fact the request failed
    to provide: the engine's own error is "<nil> * float64" plus the whole
    expression, which reads like a syntax error and sends the operator looking
    in the wrong place.
    """
    status, body = submit(PER_SECOND, prompt="no duration")
    message = str(body.get("message") or "")
    ok = check("missing duration is rejected and names the missing fact",
               status == 400 and body.get("code") == "model_price_error"
               and "seconds" in message and "usage facts" in message,
               f"{status} {message[:100]}")
    return ok, message


def start_case(model, fields):
    status, body = submit(model, **fields)
    if status != 202 or not body.get("task_id"):
        return {"error": f"submit {status} {body}"}
    return {"task_id": body["task_id"]}


def finish_case(case):
    task_id = case.get("task_id")
    if not task_id:
        return check(case["name"], False, case.get("error", "no task id"))
    status, body = poll_client(task_id)
    if status != 200:
        return check(case["name"], False, f"poll {status} {str(body)[:120]}")
    ok, state = wait_settled(task_id, case["want"])
    return check(case["name"], ok,
                 f"quota={state[1] if state else 'missing'} want={case['want']} task={task_id}")


def quota_balance():
    rows = db(f"SELECT quota FROM users WHERE id={BILL_USER_ID};")
    user_quota = int(rows[0][0]) if rows else -1
    rows = db(f"SELECT remain_quota FROM tokens WHERE id={BILL_TOKEN_ID};")
    token_quota = int(rows[0][0]) if rows else -1
    return user_quota, token_quota


def main():
    print("== setup")
    if not wait_ready(timeout=10):
        return check("gateway is up", False, BASE)
    check("gateway is up", True, BASE)
    db(setup_sql())
    if not restart_app():
        return check("app restarted with the new channel", False, APP)
    check("app restarted with the new channel", True, APP)
    admin = admin_login()
    if not save_expressions(admin):
        return False

    print("== guard")
    ok, _ = probe_missing_duration()

    print("== billing")
    # (name, model, fields, settled quota, reserved quota at submit). The two
    # token cases reserve at the call tier because tokens are unknown until the
    # completion reply; every other case reserves exactly what it settles.
    cases = [
        ("per call: 0.10 per request, duration ignored", PER_CALL, {"duration": 5}, quota(0.10), quota(0.10)),
        ("per call: same price whatever the duration", PER_CALL, {"duration": 30}, quota(0.10), quota(0.10)),
        ("per second: 5s x 720p(2) x 0.01", PER_SECOND, {"duration": 5, "resolution": "720p"},
         quota(5 * 2 * 0.01), quota(5 * 2 * 0.01)),
        ("per second: 2s x 480p(1) x 0.01", PER_SECOND, {"duration": 2, "resolution": "480p"},
         quota(2 * 1 * 0.01), quota(2 * 1 * 0.01)),
        ("per token: 5s -> 120000 tokens x $10/1M", PER_TOKEN, {"duration": 5},
         quota(token_count(5) * 10.0 / 1_000_000), quota(0.20)),
        ("per token: 2s -> 48000 tokens x $10/1M", PER_TOKEN, {"duration": 2},
         quota(token_count(2) * 10.0 / 1_000_000), quota(0.20)),
        # 上游不报用量：退到按次（$0.20），时长不参与。
        ("no usage falls back to per call", NO_USAGE, {"duration": 5}, quota(0.20), quota(0.20)),
        ("no usage ignores duration in the call tier", NO_USAGE, {"duration": 30}, quota(0.20), quota(0.20)),
        ("legacy per_call channel path is unchanged", LEGACY, {"duration": 5}, quota(0.01), quota(0.01)),
        ("legacy per_second channel path is unchanged", LEGACY_SECOND, {"duration": 5, "resolution": "480p"},
         quota(5 * 0.01), quota(5 * 0.01)),
    ]
    # 先全部提交再统一等待：轮询器是批量扫的，串行等待会把墙钟拖到八倍。
    started = []
    for name, model, fields, want, reserved in cases:
        case = start_case(model, fields)
        case.update({"name": name, "want": want, "reserved": reserved})
        started.append(case)
    for case in started:
        ok &= finish_case(case)

    expected_spend = sum(case["want"] for case in started)
    user_quota, token_quota = quota_balance()
    ok &= check("user quota is debited by the settled total",
                user_quota == START_QUOTA - expected_spend,
                f"user={user_quota} want={START_QUOTA - expected_spend}")
    ok &= check("token quota matches the user balance",
                token_quota == START_QUOTA - expected_spend,
                f"token={token_quota} want={START_QUOTA - expected_spend}")

    # C8: every per-token task left a settlement row. The submission row carries
    # the same task_id with quota 0, so the settlement row is the one that has
    # actual_quota at all. A token price reserves at the seconds tier (the chain
    # falls through), so the row's own quota is the difference, not the total.
    token_cases = [case for case in started if case["want"] != 0 and case.get("task_id")
                   and case["name"].startswith("per token")]
    ids = ",".join(f"'{case['task_id']}'" for case in token_cases)
    rows = ch(
        "SELECT JSONExtractString(other,'task_id'), "
        "JSONExtractInt(other,'pre_consumed_quota'), "
        "JSONExtractInt(other,'actual_quota') "
        "FROM logs WHERE type=2 AND JSONExtractString(other,'actual_quota') != '' "
        f"AND JSONExtractString(other,'task_id') IN ({ids})"
    )
    recorded = {row[0]: (int(row[1]), int(row[2])) for row in rows}
    ok &= check("each token settlement records its reservation and actual quota",
                len(token_cases) == 2 and all(
                    recorded.get(case["task_id"]) == (case["reserved"], case["want"])
                    for case in token_cases),
                json.dumps(recorded))

    # C14: a task that fell through to the non-token tier leaves an audit row.
    # It cannot be inferred from the money: the fallback charge equals the
    # reservation, so no consume row is written at all.
    fallback_cases = [case for case in started if case["name"].startswith("no usage") and case.get("task_id")]
    audited_ids = ",".join(f"'{case['task_id']}'" for case in fallback_cases + token_cases)
    rows = ch(
        "SELECT JSONExtractString(other,'task_id'), JSONExtractString(other,'usage_fallback'), "
        "JSONExtractInt(other,'pre_consumed_quota'), JSONExtractInt(other,'actual_quota') "
        "FROM logs WHERE type=4 AND JSONExtractString(other,'usage_fallback') != '' "
        f"AND JSONExtractString(other,'task_id') IN ({audited_ids})"
    )
    audited = {row[0]: (row[1], int(row[2]), int(row[3])) for row in rows}
    ok &= check("fallback settlement leaves an audit row, token settlement does not",
                len(fallback_cases) == 2
                and all(audited.get(case["task_id"]) == ("tokens_missing", case["reserved"], case["want"])
                        for case in fallback_cases)
                and not any(case["task_id"] in audited for case in token_cases),
                json.dumps(audited))

    print("==", "PASS" if ok else "FAIL")
    return ok


if __name__ == "__main__":
    sys.exit(0 if main() else 1)
