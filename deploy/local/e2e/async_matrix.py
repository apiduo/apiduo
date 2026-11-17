#!/usr/bin/env python3
"""End-to-end matrix for the asynchronous Images endpoint.

One scenario per run so a failure names its own root cause:

    python3 deploy/local/e2e/async_matrix.py s1   # upstream async passthrough
    python3 deploy/local/e2e/async_matrix.py s2   # gateway-side local execution
    python3 deploy/local/e2e/async_matrix.py s3   # 8001 fails, local takes over
    python3 deploy/local/e2e/async_matrix.py s4   # worker scheduling under load
    python3 deploy/local/e2e/async_matrix.py s5   # a hostile result url

`setup` (re)creates the channels the scenarios need and restarts the app so its
channel cache sees them. Exit code 0 means the scenario passed.
"""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
BASE = os.environ.get("APIDUO_BASE", "http://127.0.0.1:39047")
TOKEN = os.environ.get("APIDUO_TOKEN", "")


def token():
    """本地测试令牌：优先环境变量，其次从本地库取，不把密钥写进仓库。"""
    global TOKEN
    if not TOKEN:
        rows = db("SELECT `key` FROM tokens WHERE status=1 ORDER BY id LIMIT 1;")
        TOKEN = rows[0][0] if rows else ""
    return TOKEN
MYSQL = os.environ.get("APIDUO_MYSQL", "apiduo-local-mysql")
DB_NAME = os.environ.get("APIDUO_DB", "apiduo_local")
APP = os.environ.get("APIDUO_APP", "apiduo-local-app")
MOCK = os.environ.get("APIDUO_MOCK", "apiduo-local-mock")
ASYNC = "/async/v1/images/generations"

SETUP_SQL = """
DELETE FROM abilities WHERE model IN ('hailuo-sync','hailuo-fallback','slow-test','slow-test-b','flaky-test','limited-test','rotate-test','public-model','vendor-x','tier-test','url-bad');
DELETE FROM channels WHERE name IN ('hailuo-sync','fallback-async-fail','hailuo-sync-fallback','slow-sync','slow-sync-b','flaky-sync','limited-sync','rotate-a','rotate-b','alias-a','alias-b','tier-a','tier-b','tier-c','bad-url-async');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,model_mapping)
VALUES (1,'sk-hailuo',1,'hailuo-sync',1,UNIX_TIMESTAMP(),'https://hailuo.1task.cn','hailuo-sync','default',0,0,
        '{"hailuo-sync":"nano_banana21_flash"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (8001,'k',1,'fallback-async-fail',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','hailuo-fallback','default',10,0,
  '{"advanced_async":{"protocol":"images","incoming_paths":[],"submit":{"upstream_path":"/mock/fail-500","method":"POST"},"query":{"upstream_path":"/mock/images/query/{task_id}","method":"GET"},"fields":{"task_id_path":["task_id"],"status_path":["status"],"result_url_path":["url"]},"billing":{"mode":"per_call"}}}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,model_mapping)
VALUES (1,'sk-hailuo',1,'hailuo-sync-fallback',1,UNIX_TIMESTAMP(),'https://hailuo.1task.cn','hailuo-fallback','default',0,0,
        '{"hailuo-fallback":"nano_banana21_flash"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'slow-sync',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','slow-test','default',0,0,
        '{"primary_endpoint":"/mock/images/slow"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'slow-sync-b',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','slow-test-b','default',0,0,
        '{"primary_endpoint":"/mock/images/slow"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'flaky-sync',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','flaky-test','default',0,0,
        '{"primary_endpoint":"/mock/images/flaky"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'limited-sync',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','limited-test','default',0,0,
        '{"primary_endpoint":"/mock/images/slow","local_worker":{"concurrency":2}}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'rotate-a',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','rotate-test','default',10,0,
        '{"primary_endpoint":"/mock/images/denied"}');

-- rotate-b 带模型映射：运营配它就是为了让它能服务 rotate-test。
INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings,model_mapping)
VALUES (1,'k',1,'rotate-b',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','rotate-test','default',0,0,
        '{"primary_endpoint":"/mock/images/slow"}','{"rotate-test":"slow-test"}');

-- 只暴露上游名、没有映射的渠道：客户端请求 public-model 时它不在候选里，
-- 只有用「这条任务解析出的上游名」去找才会命中。
INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings,model_mapping)
VALUES (1,'k',1,'alias-a',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','public-model','default',10,0,
        '{"primary_endpoint":"/mock/images/denied"}','{"public-model":"vendor-x"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'alias-b',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','vendor-x','default',0,0,
        '{"primary_endpoint":"/mock/images/slow"}');

-- 三个优先级档，前两档都限流：验证据「优先级降序」逐档尝试。
INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'tier-a',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','tier-test','default',20,0,
        '{"primary_endpoint":"/mock/images/denied"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'tier-b',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','tier-test','default',10,0,
        '{"primary_endpoint":"/mock/images/denied"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (1,'k',1,'tier-c',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','tier-test','default',0,0,
        '{"primary_endpoint":"/mock/images/slow"}');

INSERT INTO channels (type,`key`,status,name,weight,created_time,base_url,models,`group`,priority,auto_ban,settings)
VALUES (8001,'k',1,'bad-url-async',1,UNIX_TIMESTAMP(),'http://mock-upstream:18080','url-bad','default',0,0,
  '{"advanced_async":{"protocol":"images","incoming_paths":[],"submit":{"upstream_path":"/mock/bad-url/submit","method":"POST"},"query":{"upstream_path":"/mock/bad-url/query/{task_id}","method":"GET"},"fields":{"task_id_path":["task_id"],"status_path":["status"],"result_url_path":["url"]},"billing":{"mode":"per_call"}}}');

INSERT INTO abilities (`group`,model,channel_id,enabled,priority,weight)
SELECT 'default', models, id, 1, priority, 1 FROM channels
WHERE name IN ('hailuo-sync','fallback-async-fail','hailuo-sync-fallback','slow-sync','slow-sync-b','flaky-sync','limited-sync','rotate-a','rotate-b','alias-a','alias-b','tier-a','tier-b','tier-c','bad-url-async');

INSERT INTO options (`key`,`value`) VALUES
 ('ModelPrice','{"flux-mock":0.01,"nano_banana21_flash":0.04,"retry-test":0.01,"local-test":0.02,"vendor-test":0.02,"hailuo-sync":0.04,"hailuo-fallback":0.04,"slow-test":0.01,"slow-test-b":0.01,"flaky-test":0.01,"limited-test":0.01,"rotate-test":0.01,"public-model":0.01,"vendor-x":0.01,"tier-test":0.01,"url-bad":0.01}')
ON DUPLICATE KEY UPDATE `value`=VALUES(`value`);
INSERT INTO options (`key`,`value`) VALUES ('RetryTimes','1')
ON DUPLICATE KEY UPDATE `value`=VALUES(`value`);
"""


def mysql_password():
    path = os.path.join(ROOT, "deploy", "local", ".env")
    with open(path, "r", encoding="utf-8") as handle:
        for line in handle:
            if line.startswith("MYSQL_ROOT_PASSWORD="):
                return line.split("=", 1)[1].strip()
    raise SystemExit("MYSQL_ROOT_PASSWORD not found in deploy/local/.env")


def db(sql, timeout=30):
    out = subprocess.run(
        ["docker", "exec", "-i", MYSQL, "mysql", "-uroot", f"-p{mysql_password()}",
         "-N", "--batch", DB_NAME],
        input=sql, capture_output=True, text=True, timeout=timeout,
    )
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip())
    return [line.split("\t") for line in out.stdout.splitlines() if line]


def docker(*args, timeout=60):
    return subprocess.run(["docker", *args], capture_output=True, text=True, timeout=timeout)


def call(method, path, body=None, timeout=120):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(BASE + path, data=data, method=method)
    request.add_header("Authorization", "Bearer " + token())
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


def mock_reset():
    """清空 mock 的计数器（它直接对上游说话，不走网关）。"""
    request = urllib.request.Request("http://127.0.0.1:18080/mock/reset", data=b"{}", method="POST")
    request.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(request, timeout=10) as response:
        response.read()


def submit(model, prompt="a red cube"):
    return call("POST", ASYNC, {"model": model, "prompt": prompt})


def poll(task_id, timeout=300, interval=3):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        status, body = call("GET", f"{ASYNC}/{task_id}", timeout=30)
        last = (status, body)
        if status == 200:
            return last
        time.sleep(interval)
    return last


def task_row(task_id):
    rows = db(f"SELECT status, execution_mode, platform, channel_id, quota, "
              f"JSON_UNQUOTE(JSON_EXTRACT(private_data,'$.result_url')), "
              f"JSON_UNQUOTE(JSON_EXTRACT(private_data,'$.execution.upstream_url')), "
              f"JSON_UNQUOTE(JSON_EXTRACT(private_data,'$.execution.origin_request.model')), "
              f"fail_reason, "
              f"JSON_UNQUOTE(JSON_EXTRACT(properties,'$.origin_model_name')), "
              f"JSON_UNQUOTE(JSON_EXTRACT(properties,'$.upstream_model_name')) "
              f"FROM tasks WHERE task_id={sql_str(task_id)};")
    return rows[0] if rows else None


def sql_str(value):
    return "'" + str(value).replace("'", "''") + "'"


def wait_ready(timeout=90):
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
    docker("restart", APP, timeout=90)
    return wait_ready()


def drain(timeout=180):
    """等到没有排队的本地任务，让场景之间互不影响（准入会拦住满队列的提交）。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        pending = db("SELECT COUNT(*) FROM tasks WHERE execution_mode='local' "
                     "AND status IN ('NOT_START','QUEUED');")
        if pending and pending[0][0] == "0":
            return True
        time.sleep(3)
    return False


def check(name, ok, detail=""):
    print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f"  [{detail}]" if detail else ""))
    return ok


# ── scenarios ────────────────────────────────────────────────────────────────

def s1():
    """上游异步透传：8001 渠道自己说话，网关只转发任务号与状态。"""
    status, body = submit("nano_banana21_flash")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False
    status, body = poll(task_id)
    ok &= check("poll reaches 200", status == 200, f"{status} {body}")
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("result url is http(s)", url.startswith("http"), url)
    row = task_row(task_id)
    ok &= check("task is not local", row and row[1] != "local", row[1] if row else "missing")
    ok &= check("platform is 8001", row and row[2] == "8001", row[2] if row else "missing")
    return ok


def s2():
    """网关侧同步转异步：同步渠道被选中，worker 重放，客户端形状不变。"""
    status, body = submit("hailuo-sync")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False
    row = task_row(task_id)
    ok &= check("execution_mode is local", row and row[1] == "local", row[1] if row else "missing")
    upstream = row[6] if row else ""
    ok &= check("upstream url is the sync images path",
                upstream.endswith("/v1/images/generations"), upstream)
    # 存的永远是客户端原文；映射后的名字落在 properties 里，由执行期算出。
    ok &= check("stored request keeps the client's model",
                row and row[7] == "hailuo-sync", row[7] if row else "missing")
    ok &= check("the channel's mapping produced the upstream name",
                row and row[10] == "nano_banana21_flash", row[10] if row else "missing")
    status, body = poll(task_id)
    ok &= check("poll reaches 200", status == 200, f"{status} {body}")
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("result url is http(s)", url.startswith("http"), url)
    return ok


def s3():
    """8001 先失败，重试落到同步渠道，客户端只看到一次受理。"""
    marker = int(time.time())
    docker("exec", MOCK, "sh", "-c", ": > /tmp/mock_upstream.log")
    status, body = submit("hailuo-fallback")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False
    log = docker("exec", MOCK, "cat", "/tmp/mock_upstream.log").stdout
    ok &= check("first attempt hit the failing 8001 endpoint", "/mock/fail-500" in log, log.strip()[:160])
    row = task_row(task_id)
    ok &= check("a local task was created", row and row[1] == "local", row[1] if row else "missing")
    status, body = poll(task_id)
    ok &= check("poll reaches 200", status == 200, f"{status} {body}")
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("result url is http(s)", url.startswith("http"), url)
    duplicates = db(f"SELECT COUNT(*) FROM tasks WHERE execution_mode='local' AND submit_time >= {marker - 5};")
    ok &= check("exactly one local task for one submission", duplicates and duplicates[0][0] == "1",
                duplicates[0][0] if duplicates else "missing")
    return ok


def s4():
    """调度器不被单个任务占住：8 个 20 秒任务应在一分钟内并发跑完。"""
    drain()
    started = time.time()
    ids = []
    for _ in range(8):
        status, body = submit("slow-test", "perf")
        if status != 202 or not body.get("task_id"):
            return check("submit returns 202", False, f"{status} {body}")
        ids.append(body["task_id"])
    check("8 submissions accepted", len(ids) == 8)

    deadline = time.time() + 120
    pending = set(ids)
    while pending and time.time() < deadline:
        for task_id in list(pending):
            status, body = call("GET", f"{ASYNC}/{task_id}", timeout=30)
            if status == 200:
                pending.discard(task_id)
        if pending:
            time.sleep(2)
    elapsed = time.time() - started
    ok = check("all 8 finished", not pending, f"{len(pending)} pending after {elapsed:.0f}s")
    ok &= check("finished within 60s", elapsed <= 60, f"{elapsed:.0f}s")

    # A blocked scheduler shows up as a gap between rows, not as a low count:
    # rows are created before the pass and claimed inside it, so counting them
    # undercounts by one and misses nothing important.
    rows = db(f"SELECT created_at FROM system_tasks WHERE type='async_task_poll' "
              f"AND created_at >= {int(started) - 30} ORDER BY created_at;")
    stamps = [int(row[0]) for row in rows]
    gap = max((b - a for a, b in zip(stamps, stamps[1:])), default=0)
    ok &= check("scheduler never stalled", gap <= 45, f"max gap {gap}s over {len(stamps)} rows")
    return ok


def s5():
    """上游给一个 javascript: 地址时，它不能到达客户端。"""
    status, body = submit("url-bad")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False
    status, body = poll(task_id, timeout=120)
    raw = json.dumps(body)
    ok &= check("client never receives the hostile url", "javascript:" not in raw, raw[:200])
    ok &= check("task fails instead of hanging", body.get("status") == "failure", raw[:200])
    if status == 200:
        url = (body.get("data") or [{}])[0].get("url", "")
        ok &= check("200 carries no result url", not url, url)
    return ok


def s6():
    """容量：24 个 20 秒任务（槽位的 3 倍），测排队而不是丢任务。"""
    drain()
    total = 24
    started = time.time()
    ids = []
    for _ in range(total):
        status, body = submit("slow-test", "load")
        if status != 202 or not body.get("task_id"):
            return check("submissions accepted", False, f"{status} {body}")
        ids.append(body["task_id"])

    deadline = time.time() + 240
    pending = set(ids)
    while pending and time.time() < deadline:
        for task_id in list(pending):
            status, _ = call("GET", f"{ASYNC}/{task_id}", timeout=30)
            if status == 200:
                pending.discard(task_id)
        if pending:
            time.sleep(2)
    elapsed = time.time() - started
    ok = check(f"all {total} finished", not pending, f"{len(pending)} pending after {elapsed:.0f}s")

    row = db(f"SELECT status, COUNT(*) FROM tasks WHERE submit_time >= {int(started) - 5} "
             f"GROUP BY status;")
    counts = {status: int(count) for status, count in row}
    ok &= check("none lost or duplicated", counts.get("SUCCESS", 0) == total, json.dumps(counts))
    # 24 / 8 slots × 20s = 60s，加一轮调度与网络开销。
    ok &= check("throughput matches the slot count", elapsed <= 110, f"{elapsed:.0f}s")
    return ok


def s7():
    """渠道隔离：一个慢渠道不能把全部槽位吃光。"""
    for _ in range(8):
        status, body = submit("slow-test", "fair-a")
        if status != 202:
            return check("channel A accepted", False, f"{status} {body}")
    time.sleep(1)
    b_ids = []
    for _ in range(8):
        status, body = submit("slow-test-b", "fair-b")
        if status != 202:
            return check("channel B accepted", False, f"{status} {body}")
        b_ids.append(body["task_id"])

    started = time.time()
    first_b = None
    while time.time() - started < 60:
        for task_id in b_ids:
            status, _ = call("GET", f"{ASYNC}/{task_id}", timeout=20)
            if status == 200:
                first_b = time.time() - started
                break
        if first_b is not None:
            break
        time.sleep(2)
    # A 占满槽位时，B 要等 A 的一整轮（8×20s/8 槽 ≈ 20s，再排一轮就到 40s+）。
    # 隔离后 B 应立即开工：首个完成 ≈ 20 秒。
    return check("channel B is not starved by channel A", first_b is not None and first_b <= 30,
                 f"first B finished after {first_b if first_b is None else round(first_b)}s")


def s8():
    """准入控制：队列有上限，满了要拒绝而不是无限受理。"""
    # 默认每人 20 条排队额度；8 个在跑不占队列，所以要先把它填满。
    drain()
    accepted, rejected, codes = 0, 0, {}
    for _ in range(40):
        status, body = call("POST", ASYNC, {"model": "slow-test", "prompt": "admission"})
        if status == 202:
            accepted += 1
        else:
            rejected += 1
            code = body.get("code") or body.get("error", {}).get("code", "")
            codes[code] = codes.get(code, 0) + 1
    ok = check("over-limit submissions are rejected", rejected > 0, f"accepted={accepted} rejected={rejected} {codes}")
    ok &= check("rejection says the queue is full", codes.get("queue_full", 0) > 0, json.dumps(codes))
    return ok


def s9():
    """时钟：认领写入的 start_time 与回收的截止点必须同源（都用数据库时钟）。"""
    drain()
    status, body = submit("slow-test", "clock")
    task_id = body.get("task_id")
    if status != 202 or not task_id:
        return check("submit", False, f"{status} {body}")
    # 认领可能晚一拍（重启后调度要排空旧租约），等到 start_time 落库再比。
    deadline = time.time() + 60
    row = None
    while time.time() < deadline:
        row = db(f"SELECT start_time, (SELECT UNIX_TIMESTAMP()) FROM tasks WHERE task_id='{task_id}';")
        if row and int(row[0][0]) > 0:
            break
        time.sleep(2)
    if not row:
        return check("task row exists", False)
    start, db_now = int(row[0][0]), int(row[0][1])
    drift = abs(db_now - start)
    # 同一秒内写入是正确的表现，不是异常：drift=0 就是同源。
    return check("start_time comes from the database clock", drift <= 5,
                 f"start={start} db_now={db_now} drift={drift}s")


def s10():
    """限流弹性：没有别的渠道可换时，workes 等上游缓过来再试，而不是退款。"""
    drain()
    docker("exec", MOCK, "sh", "-c", ": > /tmp/mock_upstream.log")
    mock_reset()
    started = time.time()
    status, body = submit("flaky-test", "elastic")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False

    status, body = poll(task_id, timeout=180)
    elapsed = time.time() - started
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("task succeeds after the vendor frees up", status == 200 and url.endswith("/flaky.png"),
                json.dumps(body)[:160])

    log = docker("exec", MOCK, "cat", "/tmp/mock_upstream.log").stdout
    posts = sum(1 for line in log.splitlines() if line.startswith("POST /mock/images/flaky"))
    ok &= check("it tried three times", posts == 3, f"{posts} POSTs")
    # 退避是 8 秒 + 16 秒；没有等待意味着在空转打上游。
    ok &= check("it waited between attempts", elapsed >= 20, f"{elapsed:.1f}s")
    return ok


def s11():
    """同步入口保持同步：不能因为渠道能被异步端点选中，就把同步请求也变成任务。"""
    drain()
    before = db("SELECT COUNT(*) FROM tasks;")[0][0]
    docker("exec", MOCK, "sh", "-c", ": > /tmp/mock_upstream.log")

    started = time.time()
    status, body = call("POST", "/v1/images/generations",
                        {"model": "slow-test", "prompt": "sync stays sync"})
    elapsed = time.time() - started

    ok = check("sync endpoint answers 200, not 202", status == 200, f"{status} {body}")
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("the body is an image, not a task id", url.endswith("/sync.png"), url)
    # local 执行会打渠道的主端点（20 秒的慢端点）；同步中继打的是 images 路径。
    ok &= check("answered synchronously", elapsed < 5, f"{elapsed:.1f}s")

    after = db("SELECT COUNT(*) FROM tasks;")[0][0]
    ok &= check("no task row was created", before == after, f"{before} -> {after}")

    log = docker("exec", MOCK, "cat", "/tmp/mock_upstream.log").stdout
    ok &= check("upstream saw the sync path", "/v1/images/generations" in log, log.strip()[:100])
    ok &= check("the worker was not involved", "/mock/images/slow" not in log, log.strip()[:100])
    return ok


def mock_stats():
    request = urllib.request.Request("http://127.0.0.1:18080/mock/stats", method="GET")
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.loads(response.read())


def s13():
    """渠道级并发：渠道声明 concurrency=2，worker 就不能并发 3 个。"""
    drain()
    mock_reset()
    ids = []
    for _ in range(6):
        status, body = submit("limited-test", "limited")
        if status != 202:
            return check("submissions accepted", False, f"{status} {body}")
        ids.append(body["task_id"])

    deadline = time.time() + 120
    pending = set(ids)
    while pending and time.time() < deadline:
        for task_id in list(pending):
            status, _ = call("GET", f"{ASYNC}/{task_id}", timeout=20)
            if status == 200:
                pending.discard(task_id)
        if pending:
            time.sleep(2)

    ok = check("all tasks finished", not pending, f"{len(pending)} pending")
    stats = mock_stats()
    # 上限是 2；若读不到渠道配置，这里会看到 7（进程份额）。
    ok &= check("the channel's own limit was honoured", stats["max_concurrency"] == 2,
                json.dumps(stats))
    return ok


def s12():
    """429 换渠道：高优先级渠道限流后，任务换到另一个渠道完成，而不是退款。"""
    drain()
    mock_reset()
    docker("exec", MOCK, "sh", "-c", ": > /tmp/mock_upstream.log")

    status, body = submit("rotate-test", "failover")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False

    status, body = poll(task_id, timeout=180)
    raw = json.dumps(body)
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("task succeeds on the second channel", status == 200 and url.endswith("/slow.png"), raw[:160])

    log = docker("exec", MOCK, "cat", "/tmp/mock_upstream.log").stdout
    ok &= check("the limited channel was tried once", log.count("denied image rate limited") == 1,
                f"{log.count('denied image rate limited')} denials")
    ok &= check("the task moved to the other channel", "/mock/images/slow" in log, log.strip()[:120])
    # rotate-b 把 rotate-test 映射成 slow-test：不重新映射就会带着旧名字打过去。
    ok &= check("the body was re-mapped for the new channel", "model=slow-test" in log,
                [line for line in log.splitlines() if "slow image start" in line][:1])

    row = db(f"SELECT channel_id FROM tasks WHERE task_id='{task_id}';")
    # rotate-b 的优先级更低，提交时不会选中它 —— 落在这里只可能是轮换的结果。
    ok &= check("the task ended on the fallback channel",
                row and row[0][0] == str(db("SELECT id FROM channels WHERE name='rotate-b';")[0][0]),
                row[0][0] if row else "missing")
    tried = db(f"SELECT JSON_EXTRACT(private_data,'$.execution.tried_channels') FROM tasks WHERE task_id='{task_id}';")
    ok &= check("the exhausted channel is recorded", tried and tried[0][0] not in ("null", "[]"),
                tried[0][0] if tried else "missing")
    return ok


def s14():
    """选渠道只认客户端模型名：只暴露上游名的渠道不得替它干活。"""
    drain()
    mock_reset()
    docker("exec", MOCK, "sh", "-c", ": > /tmp/mock_upstream.log")

    # public-model 只由 alias-a 服务，它映射成 vendor-x 且永远 429；
    # alias-b 只声明 vendor-x。轮换若用上游名去找，就会把任务塞给 alias-b。
    status, body = submit("public-model", "no alias fallback")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False

    status, body = poll(task_id, timeout=180)
    ok &= check("the task fails rather than using an unapproved channel",
                body.get("status") == "failure", json.dumps(body)[:160])

    log = docker("exec", MOCK, "cat", "/tmp/mock_upstream.log").stdout
    ok &= check("the vendor-name channel was never called", "/mock/images/slow" not in log,
                [line for line in log.splitlines() if "slow image" in line][:1])
    row = db(f"SELECT channel_id FROM tasks WHERE task_id='{task_id}';")
    fallback = db("SELECT id FROM channels WHERE name='alias-b';")
    ok &= check("the task never moved to it",
                row and fallback and row[0][0] != fallback[0][0],
                row[0][0] if row else "missing")
    return ok


def s15():
    """按优先级降序换渠道：20 → 10 → 0，前两档限流，第三档出图。"""
    drain()
    mock_reset()
    docker("exec", MOCK, "sh", "-c", ": > /tmp/mock_upstream.log")

    status, body = submit("tier-test", "priority walk")
    ok = check("submit returns 202", status == 202, f"{status} {body}")
    task_id = body.get("task_id")
    if not task_id:
        return False

    status, body = poll(task_id, timeout=180)
    url = (body.get("data") or [{}])[0].get("url", "")
    ok &= check("task succeeds on the lowest tier", status == 200 and url.endswith("/slow.png"),
                json.dumps(body)[:160])

    log = docker("exec", MOCK, "cat", "/tmp/mock_upstream.log").stdout
    ok &= check("both higher tiers were tried first", log.count("denied image rate limited") == 2,
                f"{log.count('denied image rate limited')} denials")

    row = db(f"SELECT channel_id FROM tasks WHERE task_id='{task_id}';")
    lowest = db("SELECT id FROM channels WHERE name='tier-c';")
    ok &= check("the task ended on the lowest-priority channel",
                row and lowest and row[0][0] == lowest[0][0], row[0][0] if row else "missing")
    tried = db(f"SELECT JSON_EXTRACT(private_data,'$.execution.tried_channels') FROM tasks WHERE task_id='{task_id}';")
    ok &= check("both higher tiers are recorded as tried",
                tried and tried[0][0].count(",") == 1, tried[0][0] if tried else "missing")
    return ok


SCENARIOS = {"s1": s1, "s2": s2, "s3": s3, "s4": s4, "s5": s5, "s6": s6,
             "s7": s7, "s8": s8, "s9": s9, "s10": s10, "s11": s11,
             "s12": s12, "s13": s13, "s14": s14, "s15": s15}
def main():
    action = sys.argv[1] if len(sys.argv) > 1 else ""
    if action == "setup":
        db(SETUP_SQL)
        docker("restart", MOCK, timeout=60)
        ok = restart_app()
        print("READY" if ok else "APP DID NOT START")
        return 0 if ok else 1
    if action in SCENARIOS:
        if not wait_ready():
            print("app is not ready")
            return 1
        print(f"== {action} ==")
        return 0 if SCENARIOS[action]() else 1
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main())
