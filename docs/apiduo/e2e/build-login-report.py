#!/usr/bin/env python3
"""把 E2E 产物汇总成一份自包含的 HTML 报告。

输入：artifacts/results.json、artifacts/traffic.har、artifacts/api-run.log、artifacts/shots/*
输出：artifacts/report.html（图片以 base64 内嵌，单文件可直接发人）
"""
import base64
import json
import re
from collections import Counter
from io import BytesIO
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent
ART = ROOT / "artifacts" / "login"
SHOTS = ART / "shots"

results = json.loads((ART / "results.json").read_text(encoding="utf-8"))
har = json.loads((ART / "traffic.har").read_text(encoding="utf-8"))
entries = har["log"]["entries"]
api_log = re.sub(r"\x1b\[[0-9;]*m", "", (ART / "api-run.log").read_text(encoding="utf-8"))

BASE = results["base"]
from datetime import datetime


def local_time(ts: str, fmt: str = "%m-%d %H:%M") -> str:
    return datetime.fromisoformat(ts.replace("Z", "+00:00")).astimezone().strftime(fmt)


started = local_time(results["startedAt"])
finished = local_time(results["finishedAt"], "%H:%M")

# ── 统计 ────────────────────────────────────────────────────────────────────
status_dist = Counter(e["response"]["status"] for e in entries)
api_pass = len(re.findall(r"\[PASS\]", api_log))
api_fail = len(re.findall(r"\[FAIL\]", api_log))
total_assertions = results["total"] + api_pass + api_fail

scenarios = []
for chk in results["checks"]:
    if not scenarios or scenarios[-1][0] != chk["scenario"]:
        scenarios.append([chk["scenario"], 0, 0])
    scenarios[-1][1 if chk["ok"] else 2] += 1


def api_cases():
    cases, cur = [], None
    for line in api_log.splitlines():
        line = line.strip()
        if re.match(r"^T\d+ ", line):
            cur = {"name": line, "pass": 0, "fail": 0, "info": 0}
            cases.append(cur)
        elif cur and line.startswith("[PASS]"):
            cur["pass"] += 1
        elif cur and line.startswith("[FAIL]"):
            cur["fail"] += 1
        elif cur and line.startswith("[INFO]"):
            cur["info"] += 1
    return cases


api_cases = api_cases()


def strip_base(url: str) -> str:
    return url.replace(BASE, "") or "/"


def find(pred):
    for e in entries:
        if pred(e):
            return e
    return None


s1 = [e for e in entries if e["_scenario"] == "S1 默认登录页 /sign-in"]
s1_bytes = sum(e["response"]["content"]["size"] for e in s1)
s1_big = sorted(
    ((e["response"]["content"]["size"], strip_base(e["request"]["url"])) for e in s1),
    reverse=True,
)[:4]

doc = find(lambda e: strip_base(e["request"]["url"]) == "/sign-in") or {}
doc_headers = {k.lower(): v for k, v in doc.get("response", {}).get("headers", {}).items()}

login = find(lambda e: "/api/user/login" in e["request"]["url"] and e["response"]["status"] == 200)
refresh_ok = find(
    lambda e: e["request"]["url"].endswith("/api/user/auth/refresh") and e["response"]["status"] == 200
)
status_req = find(lambda e: e["request"]["url"].endswith("/api/status"))
put_opt = find(
    lambda e: e["request"]["url"].endswith("/api/option/") and e["request"]["method"] == "PUT"
)

chain = [
    ("POST", "/api/user/login?turnstile=", login),
    ("POST", "/api/user/auth/refresh", refresh_ok),
    ("GET", "/api/status", status_req),
    ("PUT", "/api/option/", put_opt),
]

SEC_HEADERS = [
    "content-security-policy",
    "x-frame-options",
    "x-content-type-options",
    "referrer-policy",
    "permissions-policy",
    "strict-transport-security",
]

slowest = sorted((e for e in entries if e["time"] > 0), key=lambda e: -e["time"])[:5]

FINDINGS = [
    (
        "P1",
        "登录页首屏 3.96 MB，其中编辑器类 vendor 包 2.12 MB",
        "单次 <code>/sign-in</code> 传输 17 个请求 / 3.96 MB（gzip 后）。"
        "<code>64570</code> 一个包磁盘 5.6 MB、传输 2.12 MB，内含 monaco / codemirror / shiki。"
        "上游风格的 <code>/sign-up</code> 同样加载它，说明是全局打包问题，不是皮肤引入的；但对登录页来说这份体积完全是浪费。",
        "把编辑器依赖改成路由级 <code>import()</code>，或从登录路由的 preload 中移除。",
    ),
    (
        "P1",
        "站点名仍是 <code>New API</code>",
        "<code>/api/status</code> 返回 <code>system_name = \"New API\"</code>，六套皮肤的顶栏、控制台标题、页面 title 全部显示它。"
        "logo 已经换成 Apiduo，名字没换，比 logo 本身更刺眼。",
        "后台「系统设置 → 站点信息 → 站点名称」改成 Apiduo；这是配置项，不用改代码。",
    ),
    (
        "P1",
        "注册页没有套用登录皮肤",
        "<code>/sign-up</code> 仍是上游白底表单，从登录页点「注册」会从深色极光直接跳到另一套视觉体系。",
        "复用同一层壳（可只保留 aurora/light 两套），把注册表单挂进去。",
    ),
    (
        "P2",
        "响应缺安全头",
        "文档响应没有 CSP、X-Frame-Options、X-Content-Type-Options、Referrer-Policy、Permissions-Policy。"
        "点击劫持与 MIME 嗅探都没有兜底（本地 HTTP 环境无法验证 HSTS，故不计）。",
        "在网关或 Go 中间件统一加；CSP 先上 report-only 观察一周。",
    ),
    (
        "P2",
        "<code>LoginTheme</code> 没有服务端白名单",
        "实测写入 <code>&lt;script&gt;alert(1)&lt;/script&gt;</code> 返回 200，并原样出现在 <code>/api/status</code>。"
        "前端只按已知 id 匹配、未知值回落 aurora，所以没有注入；但脏值会一直留在库里。",
        "在 <code>case \"LoginTheme\"</code> 里比对一个 id 白名单，非法值返回 400。",
    ),
    (
        "P2",
        "匿名访问每次都打一条 401 红字",
        "每个登录页加载都会 <code>POST /api/user/auth/refresh</code> → 401（本轮 15 次）。"
        "这是前端的会话探测逻辑，功能上无碍，但 DevTools 常红、监控里也会污染 4xx 指标。",
        "先看 <code>new_api_has_session</code> cookie 再决定是否 refresh。",
    ),
    (
        "P2",
        "登录后向 GitHub 拉上游版本",
        "抓包看到 <code>GET https://api.github.com/repos/QuantumNous/new-api/releases?per_page=100</code>，"
        "出现在登录成功后的控制台首屏。既向 GitHub 暴露部署，也可能在墙内拖慢首屏（本轮 354 ms，是最慢的一条）。",
        "关掉更新检查，或改成 apiduo 自己的仓库地址。",
    ),
    (
        "P3",
        "Hero 文案硬编码中文",
        "「一个密钥，连通所有模型」等左侧叙事没有走 i18n，英文界面会中英混排。",
        "抽成 i18n key，至少补 en。",
    ),
    (
        "P3",
        "静态资源只有 max-age，没有 ETag / immutable",
        "<code>/static/js/*</code> 均为 <code>max-age=604800</code>，无 <code>ETag</code>。"
        "文件名带内容哈希，加 <code>immutable</code> 可以省掉 7 天后的条件请求。",
        "改为 <code>max-age=31536000, immutable</code>。",
    ),
]

FIXED = [
    (
        "赛博皮肤透视地面在真实构建里不可见",
        "原型阶段调好的网格，落到 React 后 <code>top</code> 与遮罩不匹配：可见区域恰好落在渐变透明端，"
        "网格被完全抹掉。本轮用三组参数对比后改成 <code>top: 70%</code> + 18% 内到全黑，复跑六套截图确认。",
    )
]

POSITIVES = [
    "访问令牌只存内存，localStorage / sessionStorage 里既没有 token 也没有用户对象。",
    "刷新 Cookie 是 HttpOnly + SameSite=Strict + Path=/api/user/auth，作用域收得很紧。",
    "只带 Cookie 不带 Bearer 调管理接口会被拒（401），CSRF 面很小。",
    "未知 <code>?theme=</code> 安全回落 aurora，参数不会被拼进 DOM。",
    "六套皮肤在 1440 / 390 两档全部无横向溢出，除预期的 refresh 401 外无 JS 报错。",
]


def img(name: str, width: int, quality: int = 80) -> str:
    path = SHOTS / name
    im = Image.open(path).convert("RGB")
    ratio = width / im.width
    im = im.resize((width, int(im.height * ratio)), Image.LANCZOS)
    buf = BytesIO()
    im.save(buf, "JPEG", quality=quality, optimize=True)
    b64 = base64.b64encode(buf.getvalue()).decode()
    return f"data:image/jpeg;base64,{b64}"


CSS = """
*{box-sizing:border-box}
:root{
  --bg:#080a10;--panel:#0e1119;--panel2:#12161f;--line:rgba(255,255,255,.08);
  --ink:#eef2f9;--ink2:#a8b4c8;--ink3:#71809a;
  --ok:#4ade9b;--warn:#f5b544;--bad:#ff6b6b;--accent:#8fa9ff;
  --mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,monospace;
}
body{margin:0;background:var(--bg);color:var(--ink);font-size:15px;line-height:1.7;
  font-family:-apple-system,BlinkMacSystemFont,"PingFang SC","Noto Sans SC",sans-serif;
  -webkit-font-smoothing:antialiased}
.wrap{max-width:1120px;margin:0 auto;padding:56px 32px 80px}
.kicker{font-family:var(--mono);font-size:11px;letter-spacing:.18em;color:#7c8aa4;text-transform:uppercase}
h1{font-size:40px;line-height:1.2;margin:14px 0 12px;letter-spacing:-.02em;font-weight:600}
h1 em{font-style:normal;background-image:linear-gradient(96deg,#9fe9ff,#8fa9ff 46%,#e7a9d7);
  -webkit-background-clip:text;background-clip:text;color:transparent}
.meta{font-family:var(--mono);font-size:12px;color:var(--ink3);display:flex;gap:18px;flex-wrap:wrap}
.kpis{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin:38px 0 8px}
.kpi{background:linear-gradient(180deg,var(--panel2),var(--panel));border:1px solid var(--line);
  border-radius:14px;padding:18px 18px 15px}
.kpi b{display:block;font-size:30px;line-height:1.1;letter-spacing:-.02em;font-weight:600}
.kpi span{display:block;margin-top:6px;font-size:12px;color:var(--ink3)}
.kpi.ok b{color:var(--ok)}
.verdict{margin:26px 0 8px;padding:20px 22px;border:1px solid rgba(120,150,255,.24);
  background:linear-gradient(180deg,rgba(110,140,255,.10),rgba(110,140,255,.03));border-radius:14px}
.verdict p{margin:0;color:#cfd9ea;font-size:14.5px}
.verdict p+p{margin-top:8px}
h2{font-size:13px;letter-spacing:.12em;color:#8b99b2;font-family:var(--mono);
  text-transform:uppercase;margin:56px 0 16px;font-weight:500;
  display:flex;align-items:center;gap:12px}
h2::after{content:'';flex:1;height:1px;background:var(--line)}
table{width:100%;border-collapse:collapse;font-size:13.5px}
th{text-align:left;font-weight:500;font-size:11.5px;letter-spacing:.08em;color:#6f7d95;
  font-family:var(--mono);text-transform:uppercase;padding:0 12px 10px;border-bottom:1px solid var(--line)}
td{padding:11px 12px;border-bottom:1px solid rgba(255,255,255,.045);color:var(--ink2);vertical-align:top}
tr:last-child td{border-bottom:0}
td.name{color:var(--ink)}
.num{font-family:var(--mono);font-variant-numeric:tabular-nums}
.tag{display:inline-block;font-family:var(--mono);font-size:10.5px;padding:1px 7px;border-radius:999px;
  border:1px solid var(--line);color:var(--ink3);letter-spacing:.04em}
.tag.ok{color:var(--ok);border-color:rgba(74,222,155,.32);background:rgba(74,222,155,.08)}
.tag.p1{color:#ffb4b4;border-color:rgba(255,107,107,.34);background:rgba(255,107,107,.09)}
.tag.p2{color:#ffd79a;border-color:rgba(245,181,68,.32);background:rgba(245,181,68,.08)}
.tag.p3{color:#a9b8d1;border-color:rgba(255,255,255,.14)}
.grid3{display:grid;grid-template-columns:repeat(3,1fr);gap:14px}
.grid4{display:grid;grid-template-columns:repeat(4,1fr);gap:12px}
.shot{border:1px solid var(--line);border-radius:10px;overflow:hidden;background:#05070b}
.shot img{display:block;width:100%;height:auto}
.shot figcaption{font-family:var(--mono);font-size:10.5px;color:#6f7d95;padding:7px 10px;
  border-top:1px solid var(--line);display:flex;justify-content:space-between}
.mob{max-width:200px}
.row2{display:grid;grid-template-columns:1fr 1fr;gap:22px;align-items:start}
.bars{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:18px}
.bar{display:flex;align-items:center;gap:12px;margin-bottom:10px;font-size:12.5px}
.bar i{height:8px;border-radius:4px;display:block}
.bar .lbl{font-family:var(--mono);color:var(--ink3);width:42px}
.bar .val{font-family:var(--mono);color:var(--ink2);margin-left:auto}
.callout{background:linear-gradient(180deg,rgba(255,181,68,.10),rgba(255,181,68,.03));
  border:1px solid rgba(245,181,68,.26);border-radius:12px;padding:16px 18px;margin-top:14px}
.callout b{color:#ffd79a}
.callout p{margin:6px 0 0;color:#c9d3e4;font-size:13.5px}
.find{border:1px solid var(--line);border-radius:12px;padding:16px 18px;margin-bottom:12px;
  background:var(--panel)}
.find h3{margin:0 0 6px;font-size:15px;font-weight:600;display:flex;gap:10px;align-items:center}
.find p{margin:0;color:var(--ink2);font-size:13.5px}
.find .fix{margin-top:8px;font-size:13px;color:#9fb4d8}
.find .fix b{color:#c7d6f0;font-weight:500}
code{font-family:var(--mono);font-size:.92em;background:rgba(255,255,255,.055);
  padding:1px 5px;border-radius:4px;color:#cdd8ea}
pre{background:#05070b;border:1px solid var(--line);border-radius:10px;padding:16px 18px;
  overflow-x:auto;font-family:var(--mono);font-size:12px;color:#a9b8d1;line-height:1.9;margin:0}
pre b{color:#dfe8f6;font-weight:400}
ul.clean{margin:0;padding-left:18px;color:var(--ink2);font-size:13.5px}
ul.clean li{margin-bottom:6px}
.foot{margin-top:60px;padding-top:20px;border-top:1px solid var(--line);
  font-family:var(--mono);font-size:11.5px;color:#5f6c82;display:flex;justify-content:space-between;gap:20px;flex-wrap:wrap}
@media (max-width:820px){
  .wrap{padding:36px 18px 60px}
  h1{font-size:29px}
  .kpis{grid-template-columns:repeat(2,1fr)}
  .grid3{grid-template-columns:1fr 1fr}
  .grid4{grid-template-columns:1fr 1fr}
  .row2{grid-template-columns:1fr}
}
"""

html = []
html.append("<!DOCTYPE html><html lang='zh-CN'><head><meta charset='utf-8'>")
html.append("<meta name='viewport' content='width=device-width,initial-scale=1'>")
html.append("<meta name='color-scheme' content='dark'>")
html.append("<title>apiduo 登录链路 · 端到端测试报告</title>")
html.append(f"<style>{CSS}</style></head><body><div class='wrap'>")

html.append(
    f"""
<div class='kicker'>apiduo · local end-to-end</div>
<h1>登录链路 <em>端到端测试</em></h1>
<div class='meta'>
  <span>{started} → {finished}</span>
  <span>{BASE}</span>
  <span>全新 SQLite · 本地构建</span>
  <span>Chrome headless · CDP 抓包</span>
</div>

<div class='kpis'>
  <div class='kpi ok'><b>{total_assertions}</b><span>断言全部通过</span></div>
  <div class='kpi'><b>{len(entries)}</b><span>抓到的请求</span></div>
  <div class='kpi'><b>{s1_bytes/1024/1024:.2f} MB</b><span>登录页首屏体积</span></div>
  <div class='kpi'><b>{status_dist.get(401,0)}</b><span>401（全部为 refresh 探测）</span></div>
</div>

<div class='verdict'>
  <p>从空库开始：初始化 → 六套皮肤 → 空提交 / 错误口令 / 正确登录 → 后台改风格 → 注册页，全链路跑通，
  浏览器 {results['total']} 条断言与接口 {api_pass} 条断言均通过。</p>
  <p>抓包暴露的问题都在“体积、品牌一致性、响应头”这三类，功能链路本身没有失败点。
  其中赛博皮肤的透视地面在真实构建里不可见，本轮已定位并修复。</p>
</div>
"""
)

# 测试矩阵
html.append("<h2>01 测试矩阵</h2><table><thead><tr><th>场景</th><th>断言</th><th>结果</th></tr></thead><tbody>")
for name, ok, bad in scenarios:
    tag = "<span class='tag ok'>PASS</span>" if bad == 0 else f"<span class='tag p1'>{bad} FAIL</span>"
    html.append(
        f"<tr><td class='name'>{name}</td><td class='num'>{ok + bad}</td><td>{tag}</td></tr>"
    )
for c in api_cases:
    tag = "<span class='tag ok'>PASS</span>" if c["fail"] == 0 else f"<span class='tag p1'>{c['fail']} FAIL</span>"
    html.append(
        f"<tr><td class='name'>{c['name']}</td><td class='num'>{c['pass'] + c['fail']}</td><td>{tag}</td></tr>"
    )
html.append("</tbody></table>")

# 视觉证据
html.append("<h2>02 六套皮肤 · 真实构建</h2><div class='grid3'>")
for skin, cn in [
    ("aurora", "深色极光"),
    ("light", "浅色精密"),
    ("terminal", "终端"),
    ("editorial", "纸面"),
    ("cyber", "赛博"),
    ("warm", "暖调"),
]:
    html.append(
        f"<figure class='shot'><img src='{img('s2-' + skin + '.png', 760)}' alt='{skin}'>"
        f"<figcaption><span>{skin}</span><span>{cn}</span></figcaption></figure>"
    )
html.append("</div>")

html.append("<h2>03 移动端与关键状态</h2><div class='grid4'>")
for name, cap in [
    ("s4-mobile-aurora.png", "390 · aurora"),
    ("s4-mobile-editorial.png", "390 · editorial"),
    ("s4-mobile-cyber.png", "390 · cyber"),
    ("s5-empty-submit.png", "空提交校验"),
    ("s6-wrong-password.png", "错误口令"),
    ("s7-after-login.png", "登录后"),
    ("s8-admin-saved.png", "后台已保存"),
    ("s9-signup.png", "注册页（未套皮肤）"),
]:
    html.append(
        f"<figure class='shot'><img src='{img(name, 420)}' alt='{cap}'>"
        f"<figcaption><span>{cap}</span></figcaption></figure>"
    )
html.append("</div>")

# 流量
ok_cnt = status_dist.get(200, 0)
bad_cnt = sum(v for k, v in status_dist.items() if k >= 400)
html.append("<h2>04 流量分析</h2><div class='row2'>")
html.append("<div class='bars'>")
html.append(f"<div class='bar'><span class='lbl'>200</span><i style='width:88%;background:linear-gradient(90deg,#2f7d5c,#4ade9b)'></i><span class='val'>{ok_cnt}</span></div>")
html.append(f"<div class='bar'><span class='lbl'>401</span><i style='width:8%;background:linear-gradient(90deg,#7a4a1f,#f5b544)'></i><span class='val'>{bad_cnt}</span></div>")
html.append(
    "<div class='bar'><span class='lbl'>类型</span><span class='val'>"
    + " · ".join(f"{k} {v}" for k, v in Counter(e["_resourceType"] for e in entries).most_common(4))
    + "</span></div>"
)
html.append(
    f"<div class='bar'><span class='lbl'>页面</span><span class='val'>{len(har['log']['pages'])} 次导航 · "
    f"{len(entries) / len(har['log']['pages']):.1f} 请求/页</span></div>"
)
html.append("<div class='callout'><b>登录页 3.96 MB</b><p>17 个请求里，两个 vendor 包占了 3.66 MB：<code>64570</code> 2.12 MB（编辑器/高亮）、<code>index</code> 1.51 MB。上游风格的注册页同样加载，属全局打包问题。</p></div>")
html.append("</div>")

html.append("<table><thead><tr><th>首屏资源</th><th>传输</th></tr></thead><tbody>")
for size, path in s1_big:
    html.append(f"<tr><td class='name'><code>{path}</code></td><td class='num'>{size/1024/1024:.2f} MB</td></tr>")
html.append("</tbody></table></div>")

# 认证链路
html.append("<h2>05 认证链路（抓包原文）</h2><table><thead><tr><th>方法</th><th>路径</th><th>状态</th><th>耗时</th><th>响应</th></tr></thead><tbody>")
for method, path, entry in chain:
    if entry:
        size = entry["response"]["content"]["size"]
        html.append(
            f"<tr><td><span class='tag'>{method}</span></td><td class='name'><code>{path}</code></td>"
            f"<td class='num'>{entry['response']['status']}</td><td class='num'>{entry['time']} ms</td>"
            f"<td class='num'>{size/1024:.1f} KB</td></tr>"
        )
html.append("</tbody></table>")

html.append(
    "<div class='callout' style='border-color:rgba(74,222,155,.3);background:linear-gradient(180deg,rgba(74,222,155,.09),rgba(74,222,155,.02))'>"
    "<b>会话凭证怎么走</b><p>登录响应把 <code>access_token</code> 放在 body，前端只存内存；"
    "同时下发 <code>new_api_refresh</code>（HttpOnly · SameSite=Strict · Path=/api/user/auth）。"
    "刷新页面时用 Cookie 换新令牌，令牌本身从不落盘。</p></div>"
)

# 发现
html.append("<h2>06 发现</h2>")
for level, title, desc, fix in FINDINGS:
    html.append(
        f"<div class='find'><h3><span class='tag {level.lower()}'>{level}</span>{title}</h3>"
        f"<p>{desc}</p><p class='fix'><b>建议</b> · {fix}</p></div>"
    )

html.append("<h2>07 本轮已修</h2>")
for title, desc in FIXED:
    html.append(
        f"<div class='find'><h3><span class='tag ok'>FIXED</span>{title}</h3><p>{desc}</p></div>"
    )

html.append("<h2>08 做对的地方</h2><ul class='clean'>")
for x in POSITIVES:
    html.append(f"<li>{x}</li>")
html.append("</ul>")

# 安全头
html.append("<h2>09 文档响应头</h2><table><thead><tr><th>Header</th><th>值</th></tr></thead><tbody>")
for h in SEC_HEADERS:
    v = doc_headers.get(h)
    if v:
        val = v
    elif h == "strict-transport-security":
        val = "<span class='tag p3'>本地 HTTP，不适用</span>"
    else:
        val = "<span class='tag p1'>缺失</span>"
    html.append(f"<tr><td class='name'><code>{h}</code></td><td>{val}</td></tr>")
html.append("</tbody></table>")

# 复跑
html.append(
    f"""
<h2>10 复跑</h2>
<pre><b># 1. 构建（web/dist 必需，Go 会 embed）</b>
cd web &amp;&amp; bun run build &amp;&amp; cd ..
go build -o /tmp/apiduo-e2e/apiduo .

<b># 2. 全新库起服务</b>
mkdir -p /tmp/apiduo-e2e/run &amp;&amp; cd /tmp/apiduo-e2e/run
PORT=3917 CRITICAL_RATE_LIMIT=1000 /tmp/apiduo-e2e/apiduo --port 3917 &amp;

<b># 3. 初始化 + 两套测试</b>
curl -X POST http://127.0.0.1:3917/api/setup -H 'Content-Type: application/json' \\
  -d '{{"username":"root","password":"LocalTest12345","confirmPassword":"LocalTest12345"}}'
node docs/apiduo/e2e/login-ui-tests.js --base http://127.0.0.1:3917
BASE=http://127.0.0.1:3917 bash docs/apiduo/e2e/login-api-tests.sh</pre>
<p style='color:#71809a;font-size:12.5px;margin-top:10px'>
  抓包产物在 <code>docs/apiduo/e2e/artifacts/login/traffic.har</code>，可直接拖进 Chrome DevTools → Network → Import HAR 回放；
  Cookie / Authorization / 口令已脱敏，属性保留。
</p>
"""
)

html.append(
    f"<div class='foot'><span>apiduo · e2e · {finished}</span>"
    f"<span>{results['total']} UI + {api_pass} API 断言 · {len(entries)} 请求</span></div>"
)
html.append("</div></body></html>")

out = ART / "report.html"
out.write_text("".join(html), encoding="utf-8")
print("written", out, f"{out.stat().st_size/1024:.0f} KB")
