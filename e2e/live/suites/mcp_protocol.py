#!/usr/bin/env python3
"""MCP 协议端到端：跑在真网关上，渠道指向记录式假上游。

断言的是「上游实际收到什么字节」「任务记录真的推进了没有」，不看网关自己的日志 ——
那只能证明它以为自己在干什么。

env（由 run_all.sh 注入）：
  E2E_BASE       网关地址
  E2E_RELAY_KEY  中继令牌
  E2E_MCP_UP     MCP 假上游（图片同步 + 视频异步）
  E2E_ADMIN / E2E_ADMIN_PW
"""

import json
import os
import sys
import time

import httpx

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420").rstrip("/")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
UP = os.environ.get("E2E_MCP_UP", "http://127.0.0.1:39430").rstrip("/")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))

IMG = "mcp-e2e-image"
IMG_ZERO = "mcp-e2e-no-ref"
VID = "mcp-e2e-video"
OFF = "mcp-e2e-off"

rows = []


def check(name, ok, detail=""):
    rows.append((ok, name, detail))
    print(("PASS " if ok else "FAIL ") + name + ((" | " + detail) if detail else ""))


def client():
    return httpx.Client(trust_env=False, timeout=90)


def admin():
    with client() as c:
        r = c.post(f"{BASE}/api/user/login", json={"username": ADMIN[0], "password": ADMIN[1]})
        r.raise_for_status()
        return r.json()["data"]["access_token"]


def api(at, method, path, **kw):
    with client() as c:
        return c.request(method, f"{BASE}{path}", headers={"Authorization": f"Bearer {at}"}, **kw)


def up_reset():
    with client() as c:
        c.post(f"{UP}/__reset", json={})


def up_log():
    with client() as c:
        return c.get(f"{UP}/__log").json()["rows"]


def up_control(**kw):
    with client() as c:
        return c.post(f"{UP}/__control", json=kw).json()


def up_posts():
    return [r for r in up_log() if r["method"] == "POST" and not r["path"].startswith("/__")]


def mcp(method, params=None, key=None, timeout=90):
    body = {"jsonrpc": "2.0", "id": 1, "method": method}
    if params is not None:
        body["params"] = params
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    # 空串要发成「完全没带头」：客户端库会拒绝 b"Bearer " 这种非法头值，
    # 而空令牌正是要验证的场景之一。
    if key:
        headers["Authorization"] = "Bearer " + key
    with httpx.Client(trust_env=False, timeout=timeout) as c:
        r = c.post(f"{BASE}/mcp", json=body, headers=headers)
    try:
        return r.status_code, r.json()
    except Exception:
        return r.status_code, {"__raw": r.text[:400]}


def tool(name, args, key=None, timeout=90):
    status, doc = mcp("tools/call", {"name": name, "arguments": args}, key=key, timeout=timeout)
    return doc.get("result") or {}, doc.get("error"), status


def structured(name, args, key=None):
    res, err, _ = tool(name, args, key=key)
    if err:
        raise AssertionError(f"{name} 返回 JSON-RPC 错误: {err}")
    return res.get("structuredContent") or {}, res


def tool_error_text(name, args, key=None):
    """工具报错时返回文本，正常时返回空串。"""
    res, err, _ = tool(name, args, key=key)
    if err:
        return str(err.get("message") or err)
    if res.get("isError"):
        parts = [c.get("text", "") for c in (res.get("content") or []) if isinstance(c, dict)]
        return " ".join(parts)
    return ""


def wait_orders(at):
    """等待渠道进入路由表。判据用管理面，不用 /v1/models（无价模型不列出）。"""
    for _ in range(30):
        r = api(at, "GET", "/api/channel/?p=0&page_size=100")
        models = set()
        for it in ((r.json().get("data") or {}).get("items") or []):
            models.update(m.strip() for m in (it.get("models") or "").split(",") if m.strip())
        if IMG in models and VID in models:
            time.sleep(2)  # 过一个同步周期，内存缓存才换上
            return True
        time.sleep(1)
    return False


def ensure_channels(at):
    """建两条渠道：图片走 OpenAI 兼容，视频走高级异步（8001）。"""
    table = ((api(at, "GET", "/api/channel/?p=0&page_size=100").json().get("data") or {}).get("items") or [])
    for it in table:
        if it["name"] in ("mcp-e2e-img", "mcp-e2e-vid"):
            api(at, "DELETE", f"/api/channel/{it['id']}")

    img_settings = {
        "primary_endpoint": "/v1/images/generations",
        "image_protocol": {"enabled": True, "upstream": "seedream"},
        "advanced_custom": {"advanced_routes": [
            {"incoming_path": "/v1/chat/completions", "upstream_path": "/v1/chat/completions"}]},
    }
    r = api(at, "POST", "/api/channel/", json={"mode": "single", "channel": {
        "name": "mcp-e2e-img", "type": 58, "key": "sk-mcp-e2e-channel-5f3a9b1c", "base_url": UP,
        "models": f"{IMG},{IMG_ZERO}", "group": "default", "priority": 0, "weight": 0,
        "status": 1, "settings": json.dumps(img_settings)}})
    check("建图片渠道", r.status_code == 200 and r.json().get("success"), r.text[:120])

    adv = {
        "incoming_paths": ["/v1/video/generations"],
        "submit": {"upstream_path": "/v1/video/generations"},
        "query": {"upstream_path": "/v1/video/generations/{task_id}", "task_id_location": "path"},
        "fields": {"task_id_path": ["task_id"], "status_path": ["status"],
                   "result_url_path": ["url"],
                   "status_map": {"queued": "SUBMITTED", "running": "IN_PROGRESS",
                                  "succeeded": "SUCCESS", "failed": "FAILURE"}},
    }
    r = api(at, "POST", "/api/channel/", json={"mode": "single", "channel": {
        "name": "mcp-e2e-vid", "type": 8001, "key": "sk-mcp-e2e-channel-5f3a9b1c", "base_url": UP,
        "models": VID, "group": "default", "priority": 0, "weight": 0, "status": 1,
        "settings": json.dumps({"advanced_async": adv})}})
    check("建视频渠道（8001 高级异步）", r.status_code == 200 and r.json().get("success"), r.text[:120])


def ensure_profiles(at):
    items = ((api(at, "GET", "/api/mcp/profiles").json().get("data") or {}).get("items") or [])
    for it in items:
        # mcp-ui-temp-* 是 UI 套件的新建用例产物：它失败时来不及删，
        # 留着会让面板列表越来越长，看起来像「这里有脏数据」。
        if it["model_id"] in (IMG, IMG_ZERO, VID, OFF) or it["model_id"].startswith("mcp-ui-temp"):
            api(at, "DELETE", f"/api/mcp/profiles/{it['id']}")

    specs = [
        {"model_id": IMG, "display_name": "MCP 图片", "kind": "image", "protocol": "openai_image",
         "async": False, "enabled": True, "max_ref_images": 2, "max_prompt_chars": 20,
         "parameters": json.dumps({"size": {"enum": ["1K", "2K"], "default": "2K"},
                                   "n": {"min": 1, "max": 4}}),
         "summary": "端到端用图片模型", "tags": "e2e,mcp"},
        {"model_id": IMG_ZERO, "display_name": "MCP 纯文生图", "kind": "image",
         "protocol": "openai_image", "async": False, "enabled": True, "max_ref_images": 0},
        {"model_id": VID, "display_name": "MCP 视频", "kind": "video", "protocol": "openai_video",
         "async": True, "enabled": True,
         "parameters": json.dumps({"duration": {"min": 5, "max": 10}}),
         "summary": "端到端用视频模型"},
        {"model_id": OFF, "display_name": "停用的模型", "kind": "image",
         "protocol": "openai_image", "async": False, "enabled": False},
    ]
    ids = {}
    for spec in specs:
        r = api(at, "POST", "/api/mcp/profiles", json=spec)
        ok = r.status_code == 200 and r.json().get("success")
        check(f"注册声明 {spec['model_id']}", ok, r.text[:120])
        if ok:
            ids[spec["model_id"]] = r.json()["data"]["id"]
    return ids


def ensure_second_token(at):
    """第二个有效令牌：用来验证 task_id 的 HMAC 密钥确实按 api_key 派生。

    换成一个乱写的 key 是测不到的 —— 那会被鉴权层先拦下来，
    看起来「也没通过」，但通过的原因完全不对。
    """
    name = "mcp-e2e-second"
    found = None
    for page in range(1, 6):
        r = api(at, "GET", f"/api/token/?p={page}&page_size=100")
        for it in ((r.json().get("data") or {}).get("items") or []):
            if it.get("name") == name:
                found = it
                break
        if found:
            break
    if not found:
        r = api(at, "POST", "/api/token/", json={
            "name": name, "remain_quota": 0, "unlimited_quota": True,
            "expired_time": -1, "group": "default"})
        if not (r.status_code == 200 and r.json().get("success")):
            return None
        for page in range(1, 6):
            r = api(at, "GET", f"/api/token/?p={page}&page_size=100")
            for it in ((r.json().get("data") or {}).get("items") or []):
                if it.get("name") == name:
                    found = it
                    break
            if found:
                break
    if not found:
        return None
    r = api(at, "POST", f"/api/token/{found['id']}/key")
    return ((r.json().get("data") or {}).get("key")) or None


def ensure_quota(at):
    """保证测试账号余额足够。

    视频任务每次提交都预扣一次 per_call 费用（这里约 $18.75），反复回归几十次
    就会把初始额度吃光，表现为 `403 insufficient_user_quota` —— 看起来像功能坏了，
    其实是测试环境耗尽。和「放开磁盘水位」是同一类前置。
    """
    need = 500_000_000  # 约 $1000
    for _ in range(3):
        d = (api(at, "GET", "/api/user/self").json().get("data") or {})
        if (d.get("quota") or 0) >= need // 2:
            return True
        api(at, "POST", "/api/user/manage", json={
            "id": d.get("id") or 1, "action": "add_quota", "mode": "add", "value": need})
    d = (api(at, "GET", "/api/user/self").json().get("data") or {})
    return (d.get("quota") or 0) >= need // 2


def main():
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY 未设置（应由 run_all.sh 注入）")

    at = admin()
    api(at, "PUT", "/api/option/", json={"key": "performance_setting.monitor_disk_threshold", "value": "99"})
    check("测试账号余额充足（视频任务每次预扣费）", ensure_quota(at), "已充值到 $500 以上")
    up_reset()
    ensure_channels(at)
    ids = ensure_profiles(at)
    check("渠道进入路由表", wait_orders(at))

    # ── 1. 入口鉴权 ───────────────────────────────────────────────
    code, doc = mcp("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                   "clientInfo": {"name": "e2e", "version": "1"}}, key=None)
    check("空令牌被拒（不回落服务身份）", code == 401, f"status={code} body={str(doc)[:120]}")

    code, _ = mcp("tools/list", key="sk-definitely-wrong")
    check("错误令牌被拒", code == 401, f"status={code}")

    code, _ = mcp("tools/list", key="")
    check("空字符串令牌被拒", code == 401, f"status={code}")

    # ── 2. 协议握手 ───────────────────────────────────────────────
    status, doc = mcp("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                     "clientInfo": {"name": "e2e", "version": "1"}}, key=RELAY)
    info = (doc.get("result") or {}).get("serverInfo") or {}
    check("initialize 返回 serverInfo", status == 200 and info.get("name") == "apiduo-mcp",
          f"status={status} serverInfo={info}")

    _, doc = mcp("tools/list", key=RELAY)
    names = [t.get("name") for t in ((doc.get("result") or {}).get("tools") or [])]
    check("四个工具齐备", sorted(names) == ["image.generate", "models.list", "task.get", "video.generate"],
          str(names))
    check("工具都带 description（agent 靠它选工具）",
          all(t.get("description") for t in ((doc.get("result") or {}).get("tools") or [])))

    code, doc = mcp("no/such/method", key=RELAY)
    raw = doc.get("__raw") or json.dumps(doc, ensure_ascii=False)
    check("未知方法被明确拒绝（不是静默 200）", code == 400 and "unsupported" in raw,
          f"status={code} body={raw[:120]}")

    err = tool_error_text("no.such.tool", {}, key=RELAY)
    check("未知工具报错", bool(err), err[:120])

    # ── 3. models.list：只暴露已启用 ───────────────────────────────
    sc, _ = structured("models.list", {}, key=RELAY)
    got = [m["id"] for m in (sc.get("models") or [])]
    check("只列已启用声明", OFF not in got and IMG in got and VID in got, str(got))
    check("count 与列表一致", sc.get("count") == len(got), f"count={sc.get('count')} len={len(got)}")

    by_id = {m["id"]: m for m in (sc.get("models") or [])}
    check("max_ref_images=2 出现在 input",
          (by_id.get(IMG, {}).get("input") or {}).get("max_reference_images") == 2,
          json.dumps(by_id.get(IMG, {}).get("input"), ensure_ascii=False))
    check("max_ref_images=0 明确输出 0（不等于未声明）",
          (by_id.get(IMG_ZERO, {}).get("input") or {}).get("max_reference_images") == 0,
          json.dumps(by_id.get(IMG_ZERO, {}).get("input"), ensure_ascii=False))
    check("未声明 max_ref_images 时不出该键（避免被读成 0）",
          "max_reference_images" not in (by_id.get(VID, {}).get("input") or {}),
          json.dumps(by_id.get(VID, {}).get("input"), ensure_ascii=False))
    check("异步模型 execution.task=async",
          (by_id.get(VID, {}).get("execution") or {}).get("task") == "async",
          json.dumps(by_id.get(VID, {}).get("execution"), ensure_ascii=False))
    check("同步模型 execution.task=sync",
          (by_id.get(IMG, {}).get("execution") or {}).get("task") == "sync",
          json.dumps(by_id.get(IMG, {}).get("execution"), ensure_ascii=False))

    sc, _ = structured("models.list", {"kind": "video"}, key=RELAY)
    vids = sc.get("models") or []
    check("kind 过滤生效（只回 video，且含本套件声明）",
          bool(vids) and all(m.get("kind") == "video" for m in vids) and VID in [m["id"] for m in vids],
          str([m["id"] for m in vids]))

    # ── 4. image.generate：本地校验不上游 ─────────────────────────
    up_reset()
    err = tool_error_text("image.generate", {"prompt": "x", "model": "no-such-model"}, key=RELAY)
    check("未注册模型被拒", "未注册" in err or "停用" in err, err[:160])
    check("未注册模型零出站", len(up_posts()) == 0, f"出站 {len(up_posts())} 次")

    err = tool_error_text("image.generate", {"prompt": "x", "model": IMG, "size": "8K"}, key=RELAY)
    check("enum 外取值被拒", "8K" in err and "1K" in err, err[:160])
    check("越界参数零出站", len(up_posts()) == 0, f"出站 {len(up_posts())} 次")

    err = tool_error_text("image.generate",
                          {"prompt": "x", "model": IMG, "reference_images": ["a", "b", "c"]}, key=RELAY)
    check("参考图超上限被拒", "2 张" in err, err[:160])

    err = tool_error_text("image.generate",
                          {"prompt": "x", "model": IMG_ZERO, "reference_images": ["a"]}, key=RELAY)
    check("上限 0 时明确不支持参考图", "不支持" in err, err[:160])
    check("以上拦截全部零出站", len(up_posts()) == 0, f"出站 {len(up_posts())} 次")

    err = tool_error_text("image.generate",
                          {"prompt": "x", "model": IMG, "extra": {"seed": 42}}, key=RELAY)
    check("未声明参数被拒", "未声明" in err, err[:160])

    # 声明了 max_prompt_chars 就必须真的拦：只写进 model_card 不拦，
    # agent 会把它当边界，超长提示词照样打到上游换一个 400 和一次计费。
    err = tool_error_text("image.generate", {"prompt": "长" * 30, "model": IMG}, key=RELAY)
    check("提示词超声明上限被拒", "上限" in err, err[:160])
    check("提示词拦截也零出站", len(up_posts()) == 0, f"出站 {len(up_posts())} 次")

    err = tool_error_text("image.generate", {"prompt": "", "model": IMG}, key=RELAY)
    check("空提示词被拒", "prompt" in err, err[:160])

    err = tool_error_text("image.generate", {"prompt": "x", "model": VID}, key=RELAY)
    check("kind 不匹配时报「产物类型不对」", "视频" in err or "image" in err, err[:160])

    # ── 5. image.generate：成功路径与出站契约 ────────────────────
    sc, res = structured("image.generate", {"prompt": "a cat on a sofa", "model": IMG, "size": "2K"}, key=RELAY)
    check("文生图 status=completed", sc.get("status") == "completed", str(sc.get("status")))
    outs = sc.get("outputs") or []
    check("文生图有产物 url", len(outs) == 1 and outs[0].get("url", "").startswith("https://"),
          json.dumps(outs, ensure_ascii=False))

    posts = up_posts()
    one = posts[-1]["body"] if posts else {}
    check("上游收到 response_format=url（不暴露给调用方）", one.get("response_format") == "url",
          json.dumps(one, ensure_ascii=False)[:200])
    check("size 原样透传", one.get("size") == "2K", str(one.get("size")))
    check("model 用声明里的 id", one.get("model") == IMG, str(one.get("model")))
    check("没有参考图时不发 image 字段", "image" not in one, json.dumps(one, ensure_ascii=False)[:200])
    check("上游用的是渠道 key 而非用户 key（不泄露）",
          posts and "sk-mcp-e2e-channel-5f3a9b1c" in (posts[-1].get("auth") or ""), str(posts[-1].get("auth") if posts else None))

    up_reset()
    sc, _ = structured("image.generate",
                       {"prompt": "restyle", "model": IMG,
                        "reference_images": "https://cdn.example.com/a.png"}, key=RELAY)
    one = (up_posts() or [{}])[-1].get("body") or {}
    check("单张参考图统一成数组", one.get("image") == ["https://cdn.example.com/a.png"],
          json.dumps(one.get("image"), ensure_ascii=False))
    check("图生图同样返回产物", len(sc.get("outputs") or []) == 1)

    up_reset()
    structured("image.generate", {"prompt": "n=2", "model": IMG, "n": 2}, key=RELAY)
    one = (up_posts() or [{}])[-1].get("body") or {}
    check("n 在声明范围内时透传", one.get("n") == 2, str(one.get("n")))

    err = tool_error_text("image.generate", {"prompt": "x", "model": IMG, "n": 9}, key=RELAY)
    check("n 超声明上限被拒", "不能大于" in err, err[:160])

    # ── 6. 上游失败：错误可见且 key 被抹掉 ───────────────────────
    up_control(image_status=500,
               image_body={"error": {"message": "upstream exploded sk-mcp-e2e-channel-5f3a9b1c"}})
    err = tool_error_text("image.generate", {"prompt": "x", "model": IMG}, key=RELAY)
    check("上游 5xx 报错可见", "500" in err, err[:200])
    check("错误文本里的 key 被脱敏", "sk-mcp-e2e-channel" not in err, err[:200])
    up_control(image_status=200, image_body=None)

    # ── 7. video.generate + task.get 全链路 ──────────────────────
    up_reset()
    sc, res = structured("video.generate", {"prompt": "a dog running", "model": VID,
                                            "extra": {"duration": 5}}, key=RELAY)
    check("异步提交返回 working", sc.get("status") == "working", str(sc.get("status")))
    tid = sc.get("task_id") or ""
    check("返回自包含 task_id", bool(tid) and tid.count(".") == 1, tid[:40])
    check("给出轮询间隔", sc.get("poll_after_ms", 0) > 0, str(sc.get("poll_after_ms")))

    one = (up_posts() or [{}])[-1].get("body") or {}
    check("video 上游收到 duration", str(one.get("duration")) == "5",
          json.dumps(one, ensure_ascii=False)[:200])

    sc, _ = structured("task.get", {"task_id": tid}, key=RELAY)
    check("首次查询为 working（任务还在跑）", sc.get("status") == "working", str(sc.get("status")))

    deadline = time.time() + 75
    final = {}
    while time.time() < deadline:
        time.sleep(5)
        final, _ = structured("task.get", {"task_id": tid}, key=RELAY)
        if final.get("status") in ("completed", "failed"):
            break
    check("任务被轮询推进到 completed", final.get("status") == "completed", str(final.get("status")))
    fouts = final.get("outputs") or []
    check("完成态带产物地址", bool(fouts) and fouts[0].get("url", "").startswith("https://"),
          json.dumps(fouts, ensure_ascii=False))
    check("完成态不再要求轮询", not final.get("poll_after_ms"), str(final.get("poll_after_ms")))

    err = tool_error_text("task.get", {"task_id": "forged.payload"}, key=RELAY)
    check("伪造 task_id 被拒", "无效" in err or "过期" in err, err[:160])

    other = ensure_second_token(at)
    check("建第二个有效令牌", bool(other), str(other)[:12])
    if other:
        res, jerr, code = tool("task.get", {"task_id": tid}, key=other)
        text = " ".join(c.get("text", "") for c in (res.get("content") or []) if isinstance(c, dict))
        blob = text or str(jerr or "")
        check("换 key 后旧 task_id 失效（HMAC 密钥按 key 派生）",
              code == 200 and ("无效" in blob or "过期" in blob), f"status={code} {blob[:120]}")

    # ── 8. 观察数据：落库但只记结构性参数 ────────────────────────
    time.sleep(1.5)
    r = api(at, "GET", f"/api/mcp/profiles/{ids[IMG]}/observations?days=7")
    doc = r.json()
    stats = ((doc.get("data") or {}).get("stats") or [])
    sizes = {s["value"]: s for s in stats if s["param"] == "size"}
    check("观察里有 size=2K 的成功样本", sizes.get("2K", {}).get("ok", 0) >= 1,
          json.dumps(stats, ensure_ascii=False)[:240])
    check("观察统计不出空取值", all(s["value"] not in ("", "null") for s in stats),
          json.dumps(stats, ensure_ascii=False)[:240])

    r = api(at, "GET", f"/api/mcp/profiles/{ids[VID]}/audit?days=7")
    check("巡检接口可用（声明 vs 观察）", r.status_code == 200 and (r.json().get("data") or {}).get("issues") is not None,
          r.text[:160])

    print()
    if any(not ok for ok, _, _ in rows):
        failed = [n for ok, n, _ in rows if not ok]
        print(f"MCP 协议套件：{len(rows) - len(failed)} 通过 / {len(failed)} 失败")
        for n in failed:
            print("  失败：" + n)
        return 1
    print(f"MCP 协议套件：{len(rows)} 条全通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
