#!/usr/bin/env python3
"""多 key 渠道：粘性 key 映射、局部重映射、活跃上限、失败换 key。

网关在上游请求头里放的是哪把 key，只有上游知道 —— 所以断言全部基于假上游
记下的 `auth`，不看网关自己回报的 `multi_key_index`。后者只在最后做一次
"日志与事实一致" 的交叉核对。

覆盖的机制（对应 model/channel_sticky.go 与 channel_multikey_pool.go）
- sticky 的种子是 token，同一令牌永远落同一把 key
- rendezvous：禁用一把 key 只重映射它自己的种子，别人的不动
- min_active_keys 上限：超出上限的 key 是备用，永远不参与选择；
  活跃 key 被摘掉后备用 key 自动补位
- polling 模式对照：连续请求真的在轮换
- polling × min_active_keys 交叉：上限对轮询同样生效，备用 key 只在窗口滑动时顶上，
  且上限经 API 热更新后立刻改变窗口宽度
- 失败换 key：同一请求被重试时不能再用刚失败的那把（tried 集合）
- 坏 key 自动禁用：只摘掉坏的那把，渠道本身继续服务
"""
import json
import os
import re
import time
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
UP1 = os.environ.get("E2E_UP1", "http://127.0.0.1:39401")
H1 = os.environ.get("E2E_H1", "/tmp/apiduo-e2e/upstream-headers.log")

KEYS = ["sk-e2e-mk-alpha", "sk-e2e-mk-beta", "sk-e2e-mk-gamma"]
TAILS = [k[len("sk-e2e-"):] for k in KEYS]  # 与假上游的 auth 字段同形

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []
STEPS = []
JWT = ""


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=60) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw or b"{}")
        except Exception:
            return err.code, raw.decode("utf-8", "replace")


def check(name, cond, detail=""):
    STEPS.append((name, bool(cond)))
    if not cond:
        FAILS.append(f"{name}: {detail}")
    print(("  PASS  " if cond else "  FAIL  ") + name + (f"  [{detail}]" if detail else ""))


def admin_token():
    st, res = call("POST", "/api/user/login", {"username": ADMIN[0], "password": ADMIN[1]})
    if st != 200 or not res.get("success"):
        raise SystemExit(f"admin login failed: HTTP {st} {res}")
    return res["data"]["access_token"]


def ensure_disk_headroom():
    call("PUT", "/api/option/", {"key": "performance_setting.monitor_disk_threshold", "value": "99"}, JWT)
    st, res = call("GET", "/api/option/", None, JWT)
    opts = {d.get("key"): d.get("value") for d in (res.get("data") or []) if isinstance(d, dict)}
    got = opts.get("performance_setting.monitor_disk_threshold")
    if got != "99":
        raise SystemExit(f"disk threshold did not stick: {got!r}")


def set_option(key, value):
    st, res = call("PUT", "/api/option/", {"key": key, "value": value}, JWT)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set {key}={value} failed: HTTP {st} {res}")


def set_retry(times):
    st, res = call("PATCH", "/api/option/request_policy", {"options": {"RetryTimes": str(times)}}, JWT)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set RetryTimes={times} failed: HTTP {st} {res}")


def channels():
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, JWT)
    return (res.get("data") or {}).get("items") or []


def drop(prefix="e2e-mk-"):
    for item in channels():
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, JWT)


def get_channel(name):
    for item in channels():
        if item.get("name") == name:
            return item
    return None


def create_multikey(name, model, keys, mode="sticky", cap=0, base=UP1, auto_ban=0):
    body = {
        "mode": "multi_to_single",
        "multi_key_mode": mode,
        "channel": {
            "name": name, "type": 1, "key": "\n".join(keys), "base_url": base,
            "models": model, "group": "default", "status": 1, "auto_ban": auto_ban,
        },
    }
    if cap:
        body["min_active_keys"] = cap
    st, res = call("POST", "/api/channel/", body, JWT)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")
    time.sleep(2)  # 等一个缓存同步周期，否则首批请求会 503
    return get_channel(name)


def make_token(name):
    call("POST", "/api/token/", {"name": name, "remain_quota": 0, "unlimited_quota": True,
                                 "expired_time": -1, "group": "default"}, JWT)
    tok = find_token(name)
    if not tok:
        raise SystemExit(f"token {name} was not created")
    st, res = call("POST", f"/api/token/{tok['id']}/key", {}, JWT)
    return (res.get("data") or {}).get("key")


def find_token(name, page_size=100, max_pages=20):
    """按名字找令牌，必须翻页：一页只有 100 条，后面套件建的令牌会落到第二页。"""
    for page in range(max_pages):
        st, res = call("GET", f"/api/token/?p={page}&page_size={page_size}", None, JWT)
        data = res.get("data") or {}
        items = data.get("items") or []
        row = next((t for t in items if t.get("name") == name), None)
        if row:
            return row
        if len(items) < page_size:
            break
    return None


def relay(model, key, n=1, content="multi-key"):
    codes = []
    for _ in range(n):
        st, res = call("POST", "/v1/chat/completions",
                       {"model": model, "messages": [{"role": "user", "content": content}]},
                       "sk-" + key)
        codes.append((st, res))
    return codes


def log_lines(model):
    """假上游收到的、属于该模型的调用（按发生顺序）。"""
    out = []
    try:
        with open(H1, encoding="utf-8") as fh:
            for line in fh:
                if f'model="{model}"' not in line:
                    continue
                found = re.search(r'auth="([^"]*)"', line)
                out.append(found.group(1) if found else "")
    except FileNotFoundError:
        pass
    return out


def used(before, model):
    return log_lines(model)[before:]


def admin_info_of_last_log(model):
    st, res = call("GET", "/api/log/?p=0&page_size=20&type=2", None, JWT)
    rows = (res.get("data") or {}).get("items") or []
    for row in rows:
        if row.get("model_name") != model:
            continue
        other = row.get("other")
        if isinstance(other, str):
            try:
                other = json.loads(other)
            except Exception:
                other = {}
        return (other or {}).get("admin_info") or {}
    return None


def key_status(channel_id):
    st, res = call("POST", "/api/channel/multi_key/manage",
                   {"channel_id": channel_id, "action": "get_key_status", "page": 1, "page_size": 100}, JWT)
    return (res.get("data") or {}).get("keys") or []


def main():
    global JWT
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    JWT = admin_token()
    drop()
    ensure_disk_headroom()
    set_option("SelfUseModeEnabled", "true")
    set_option("AutomaticDisableChannelEnabled", "false")
    set_option("AutomaticDisableStatusCodes", "401")
    set_retry(0)

    # ---------- 1. 建渠道：多 key 元信息正确落库 ----------
    m = "mk-sticky"
    ch = create_multikey("e2e-mk-sticky", m, KEYS)
    info = (ch or {}).get("channel_info") or {}
    check("多 key 渠道已建立", bool(ch), str(ch)[:120] if not ch else "")
    check("is_multi_key = true", info.get("is_multi_key") is True, str(info.get("is_multi_key")))
    check("multi_key_size = 3", info.get("multi_key_size") == 3, str(info.get("multi_key_size")))
    check("multi_key_mode = sticky", info.get("multi_key_mode") == "sticky", str(info.get("multi_key_mode")))
    keys_now = key_status(ch["id"])
    check("三把 key 都进了密钥表", len(keys_now) == 3, f"count={len(keys_now)}")
    check("密钥表按 index 0..2 排列", [k.get("index") for k in keys_now] == [0, 1, 2],
          str([k.get("index") for k in keys_now]))

    # ---------- 2. 粘性：同一令牌 12 次只落一把 key ----------
    t1 = make_token("e2e-mk-t1")
    before = len(log_lines(m))
    codes = relay(m, t1, 12)
    hits = used(before, m)
    check("粘性请求全部 200", all(c == 200 for c, _ in codes), str(sorted({c for c, _ in codes})))
    check("同一令牌 12 次只用了 1 把 key", len(set(hits)) == 1, f"keys={sorted(set(hits))}")
    check("12 次都被上游看到", len(hits) == 12, f"lines={len(hits)}")

    # ---------- 3. 不同令牌分散到不同 key，且各自稳定 ----------
    tokens = [t1] + [make_token(f"e2e-mk-p{i}") for i in range(1, 7)]
    mapping = {}
    for idx, key in enumerate(tokens):
        mark = len(log_lines(m))
        relay(m, key, 3)
        seen = used(mark, m)
        mapping[idx] = sorted(set(seen))
    stable = all(len(v) == 1 for v in mapping.values())
    union = {v[0] for v in mapping.values() if len(v) == 1}
    check("每个令牌自身都稳定在一把 key", stable, str({k: v for k, v in mapping.items() if len(v) != 1}))
    check("7 个令牌至少落在 2 把 key 上（映射真的分散）", len(union) >= 2, f"keys={sorted(union)}")

    # ---------- 4. rendezvous：禁用一把 key 只重映射它自己的种子 ----------
    victim_tail = mapping[0][0]
    victim_index = TAILS.index(victim_tail)
    # 同一条 key 往往服务多个令牌；只有「原本不在这把 key 上」的才必须纹丝不动
    others = {i: mapping[i][0] for i in mapping if i != 0 and mapping[i][0] != victim_tail}
    mark_disable = len(log_lines(m))
    st, res = call("POST", "/api/channel/multi_key/manage",
                   {"channel_id": ch["id"], "action": "disable_key", "key_index": victim_index}, JWT)
    check("禁用 key 成功", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:100]}")
    time.sleep(1)

    mark = len(log_lines(m))
    relay(m, tokens[0], 4)
    after_victim = used(mark, m)
    check("被禁用 key 的令牌换了 key", after_victim and set(after_victim) != {victim_tail},
          f"now={sorted(set(after_victim))}")
    check("换过去之后仍然是单把（新映射也稳定）", len(set(after_victim)) == 1, str(sorted(set(after_victim))))

    stayed = True
    moved = {}
    for idx, key in others.items():
        mark = len(log_lines(m))
        relay(m, tokens[idx], 2)
        now = sorted(set(used(mark, m)))
        if now != [mapping[idx][0]]:
            stayed = False
            moved[idx] = (mapping[idx][0], now)
    check("其他令牌的映射完全没动（只在受害 key 上重映射）", stayed, str(moved))
    check("被禁用的 key 再没出现过", victim_tail not in set(used(mark_disable, m)), victim_tail)

    # 复原：启用回来
    call("POST", "/api/channel/multi_key/manage",
         {"channel_id": ch["id"], "action": "enable_key", "key_index": victim_index}, JWT)
    time.sleep(1)

    # ---------- 5. min_active_keys：超出上限的 key 是备用 ----------
    m2 = "mk-cap"
    ch2 = create_multikey("e2e-mk-cap", m2, KEYS, cap=2)
    check("活跃上限已落库", ((ch2 or {}).get("channel_info") or {}).get("min_active_keys") == 2,
          str(((ch2 or {}).get("channel_info") or {}).get("min_active_keys")))
    cap_tokens = [make_token(f"e2e-mk-c{i}") for i in range(12)]
    mark = len(log_lines(m2))
    for key in cap_tokens:
        relay(m2, key, 2)
    hits = set(used(mark, m2))
    check("上限 2 时第三把 key 从不参与", TAILS[2] not in hits, f"keys={sorted(hits)}")
    check("上限内的两把都在服务", hits == set(TAILS[:2]), f"keys={sorted(hits)}")

    # 摘掉一把活跃 key，备用 key 自动补位
    call("POST", "/api/channel/multi_key/manage",
         {"channel_id": ch2["id"], "action": "disable_key", "key_index": 0}, JWT)
    time.sleep(1)
    mark = len(log_lines(m2))
    for key in cap_tokens:
        relay(m2, key, 2)
    hits2 = set(used(mark, m2))
    check("活跃 key 被摘后备用 key 补位", TAILS[2] in hits2, f"keys={sorted(hits2)}")
    check("补位后仍不越界（只有 beta/gamma）", hits2 <= {TAILS[1], TAILS[2]}, f"keys={sorted(hits2)}")
    check("被摘掉的 key 不再出现", TAILS[0] not in hits2, TAILS[0])

    # ---------- 6. polling 模式对照：真的在轮换 ----------
    m3 = "mk-poll"
    create_multikey("e2e-mk-poll", m3, KEYS, mode="polling")
    t3 = make_token("e2e-mk-poll-t")
    mark = len(log_lines(m3))
    relay(m3, t3, 6)
    seen = used(mark, m3)
    check("polling 模式下 6 次覆盖 3 把 key", len(set(seen)) == 3, f"keys={seen}")
    check("polling 是轮转而不是随机重复", len(set(seen[:3])) == 3, f"first3={seen[:3]}")

    # ---------- 6b. polling × mini batch（交叉格）：上限必须对轮询生效 ----------
    # 单测用 TestActiveCapAppliesToPolling 钉住选择器本身（6 key / 上限 2 只出 2 把）。
    # 这一格走完整链路——表单 → API → DB → 渠道缓存——验的是模式与上限在缓存里
    # 仍是同一份数据，而不是选择器拿了别人的窗口。
    m6 = "mk-pollcap"
    six = ["sk-e2e-mk-p%d" % i for i in range(6)]
    six_tails = [key[len("sk-e2e-"):] for key in six]
    ch6 = create_multikey("e2e-mk-pollcap", m6, six, mode="polling", cap=2)
    info6 = (ch6 or {}).get("channel_info") or {}
    check("polling 渠道的模式已落库", info6.get("multi_key_mode") == "polling",
          str(info6.get("multi_key_mode")))
    check("polling 渠道的活跃上限已落库", info6.get("min_active_keys") == 2,
          str(info6.get("min_active_keys")))

    t6 = make_token("e2e-mk-pollcap-t")
    mark = len(log_lines(m6))
    relay(m6, t6, 12)
    seen = used(mark, m6)
    check("polling+上限2：12 次请求全部到达上游", len(seen) == 12, "lines=%d" % len(seen))
    check("polling+上限2：只用了窗口内两把 key", set(seen) == set(six_tails[:2]),
          "keys=%s" % sorted(set(seen)))
    check("polling+上限2：窗口外四把从未参与", not (set(seen) & set(six_tails[2:])),
          "keys=%s" % sorted(set(seen)))
    pair = seen[:2]
    check("polling+上限2：严格两把交替轮转",
          len(pair) == 2 and pair[0] != pair[1] and seen == [pair[i % 2] for i in range(len(seen))],
          "seen=%s" % seen)

    # 摘掉窗口内一把 → 窗口右移，备用 key 顶上，仍然只有两把在转。
    # 期望窗口按「剩余可用 key 的前两个」算，不假设轮询从 index 0 起步。
    first_index = six_tails.index(pair[0])
    remaining = [i for i in range(6) if i != first_index]
    expected_window = {six_tails[i] for i in remaining[:2]}
    st, res = call("POST", "/api/channel/multi_key/manage",
                   {"channel_id": ch6["id"], "action": "disable_key", "key_index": first_index}, JWT)
    check("摘掉窗口内的活跃 key 成功", st == 200 and res.get("success"),
          "HTTP %d %s" % (st, str(res)[:100]))
    time.sleep(1)
    mark = len(log_lines(m6))
    relay(m6, t6, 8)
    slid = used(mark, m6)
    # 宽度是 2：掉一把就由剩余列表的下一把顶上，窗口整体右移一格。
    # 断言「恰好等于剩余前两把」同时覆盖补位和不越界，别再单独数下标。
    check("备用 key 补进窗口（窗口滑到剩余前两把）", set(slid) == expected_window,
          "keys=%s want=%s" % (sorted(set(slid)), sorted(expected_window)))
    check("被摘掉的 key 不再参与轮询", six_tails[first_index] not in slid, six_tails[first_index])

    # 上限热更新 2 → 0：必须立刻回到「全部可用 key 参与」。
    # 0 在 GORM 的 struct Updates 里是零值，写侧最容易在这里静默丢更新，
    # 所以这条只能走 HTTP 才测得出来：写库存新值，选择器读缓存。
    st, res = call("PUT", "/api/channel/", {"id": ch6["id"], "min_active_keys": 0}, JWT)
    check("上限改成 0 被接受", st == 200 and res.get("success"),
          "HTTP %d %s" % (st, str(res)[:120]))
    time.sleep(2)
    info6 = (get_channel("e2e-mk-pollcap") or {}).get("channel_info") or {}
    check("上限 0 已落库（0 是有效值，不等于「没改」）", info6.get("min_active_keys") in (0, None),
          str(info6.get("min_active_keys")))
    mark = len(log_lines(m6))
    relay(m6, t6, 10)
    wide = used(mark, m6)
    check("上限解除后轮询扩散到全部 5 把可用 key", len(set(wide)) == 5,
          "keys=%s" % sorted(set(wide)))

    # ---------- 7. 重试不能再用刚失败的那把（tried 集合） ----------
    m4 = "mk-retry"
    create_multikey("e2e-mk-retry", m4, KEYS, auto_ban=0)
    set_retry(2)
    t4 = make_token("e2e-mk-retry-t")
    mark = len(log_lines(m4))
    st, res = relay(m4, t4, 1, content="fail please")[0]
    seen = used(mark, m4)
    check("三把 key 全失败时最终返回 5xx", st >= 500, f"HTTP {st} {str(res)[:100]}")
    check("RetryTimes=2 共 3 次尝试", len(seen) == 3, f"attempts={len(seen)}")
    check("3 次尝试用了 3 把不同的 key（不重复踩同一把）", len(set(seen)) == 3, f"keys={seen}")
    set_retry(0)

    # ---------- 8. 坏 key 自动禁用：只摘它自己，渠道继续服务 ----------
    m5 = "mk-badkey"
    bad_keys = ["sk-e2e-mk-alpha", "sk-e2e-mk-bad"]
    ch5 = create_multikey("e2e-mk-badkey", m5, bad_keys, auto_ban=1)
    set_retry(1)  # 不重试的话，第一次踩到坏 key 就把 500 抛给客户端了
    set_option("AutomaticDisableChannelEnabled", "true")
    set_option("AutomaticDisableStatusCodes", "500")
    t5s = [make_token(f"e2e-mk-b{i}") for i in range(12)]
    codes = []
    for key in t5s:
        code, _ = relay(m5, key, 1)[0]
        codes.append(code)
        time.sleep(0.1)
    check("坏 key 存在时客户端仍然全部 200（重试救回）", all(c == 200 for c in codes), str(sorted(set(codes))))
    time.sleep(2)
    statuses = {k.get("index"): k.get("status") for k in key_status(ch5["id"])}
    check("坏 key 被单独标记自动禁用（3）", statuses.get(1) == 3, str(statuses))
    check("好 key 仍是启用（1）", statuses.get(0) == 1, str(statuses))
    ch5_now = get_channel("e2e-mk-badkey") or {}
    check("渠道本身没被禁用", ch5_now.get("status") == 1, f"status={ch5_now.get('status')}")
    set_option("AutomaticDisableChannelEnabled", "false")

    # ---------- 9. 日志里的 multi_key_index 与上游实际 key 一致 ----------
    info = admin_info_of_last_log(m5)
    if info is None:
        check("日志 admin_info 可读", False, "no log row with model " + m5)
    else:
        idx = info.get("multi_key_index")
        check("日志标记了多 key 渠道", info.get("is_multi_key") is True, str(info.get("is_multi_key")))
        check("multi_key_index 在有效范围内", isinstance(idx, int) and 0 <= idx < len(bad_keys), str(idx))

    drop()
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
