#!/usr/bin/env python3
"""把 docs/apiduo/login-styles/*.html 内嵌进一个可对比的画廊页面。"""
import json, pathlib, re

ROOT = pathlib.Path(__file__).parent
STYLES = [
    ("aurora", "Aurora", "深色极光", "分屏",
     "AI 产品感最强的一版：极光背景 + 玻璃卡，左侧用一条请求链路讲清楚网关是什么。",
     ["深色", "分屏", "玻璃拟态", "主推"]),
    ("light", "Light", "浅色精密", "分屏",
     "冷灰底 + 白卡 + 1px 硬边，主色只出现在标题、按钮、焦点环三处，其余全部降噪。",
     ["浅色", "分屏", "克制"]),
    ("terminal", "Terminal", "终端", "分屏",
     "把登录页做成 shell：curl 示例、等宽标点、荧光绿。面向拿接口调模型的人。",
     ["深色", "开发者", "高辨识"]),
    ("editorial", "Editorial", "纸面编辑", "分屏",
     "宋体标题、细线、编号，表单直接排在纸上。B 端客户最容易觉得这家公司正经。",
     ["浅色", "无卡片", "稳重"]),
    ("cyber", "Cyber Grid", "赛博网格", "分屏",
     "透视地面 + 霓虹描边 + 四角标记。冲击力最强，适合发布会或活动落地页。",
     ["深色", "强视觉", "慎用"]),
    ("warm", "Warm", "暖调亲和", "分屏",
     "米色、陶土橘、深绿。把「AI 基础设施」的硬度降下来，非技术访客更放松。",
     ["浅色", "柔和", "泛用户"]),
]

meta = []
docs = {}
for i, (fid, name, cn, layout, desc, tags) in enumerate(STYLES):
    src = ROOT / f"{fid}.html"
    html = src.read_text(encoding="utf-8")
    docs[fid] = html
    meta.append({"id": fid, "no": f"0{i+1}", "name": name, "cn": cn,
                 "layout": layout, "desc": desc, "tags": tags})

data = json.dumps({"meta": meta, "docs": docs}, ensure_ascii=False, separators=(",", ":"))
data = data.replace("</", "<\\/")

TEMPLATE = r"""<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>Apiduo · 登录页六种风格</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root{
  --bg:#0a0b0e;
  --ink:#e9edf5;
  --ink-2:#98a2b5;
  --ink-3:#6b7488;
  --line:rgba(255,255,255,.08);
  --line-2:rgba(255,255,255,.14);
  --card:#111319;
  --accent:#8aa3ff;
  --mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--ink);overflow-x:hidden;
  font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text","PingFang SC","Noto Sans SC",sans-serif;
  -webkit-font-smoothing:antialiased}
body::before{content:'';position:fixed;inset:0;z-index:0;pointer-events:none;
  background:radial-gradient(70% 50% at 50% -10%,rgba(120,140,255,.10),transparent 60%)}
a{color:inherit;text-decoration:none}
button{font:inherit;color:inherit;background:none;border:0;cursor:pointer}
:focus-visible{outline:2px solid var(--accent);outline-offset:3px;border-radius:4px}

.head{position:relative;z-index:2;max-width:1240px;margin:0 auto;padding:56px 32px 34px}
.kicker{display:inline-flex;align-items:center;gap:9px;font-family:var(--mono);font-size:10.5px;
  letter-spacing:.16em;color:var(--ink-3);margin-bottom:18px}
.kicker i{width:6px;height:6px;border-radius:50%;background:linear-gradient(140deg,#38d9ee,#7c5cf6 70%,#ff8fc7)}
.head h1{margin:0 0 14px;font-size:clamp(26px,3.2vw,40px);line-height:1.2;font-weight:600;letter-spacing:-.02em}
.head p{margin:0;max-width:62ch;font-size:14px;line-height:1.85;color:var(--ink-2)}
.head p b{color:var(--ink);font-weight:500}
.head__row{display:flex;flex-wrap:wrap;gap:8px;margin-top:22px}
.chip{font-family:var(--mono);font-size:10px;letter-spacing:.06em;color:var(--ink-3);
  border:1px solid var(--line);border-radius:999px;padding:4px 10px}

.grid{position:relative;z-index:2;max-width:1240px;margin:0 auto;padding:10px 32px 40px;
  display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:22px}

.card{background:var(--card);border:1px solid var(--line);border-radius:16px;overflow:hidden;
  cursor:pointer;transition:border-color .2s,transform .28s cubic-bezier(.22,1,.36,1),box-shadow .3s}
.card:hover{border-color:var(--line-2);transform:translateY(-3px);
  box-shadow:0 24px 50px -30px rgba(0,0,0,.95),0 0 0 1px rgba(138,163,255,.14)}
.card__shot{position:relative;aspect-ratio:16/10;overflow:hidden;background:#07080b;
  border-bottom:1px solid var(--line)}
.card__shot iframe{position:absolute;top:0;left:0;width:1440px;height:900px;border:0;
  transform-origin:top left;pointer-events:none;background:#07080b}
.card__shot::after{content:'';position:absolute;inset:0;pointer-events:none;
  background:linear-gradient(180deg,transparent 62%,rgba(6,7,10,.35))}
.card__meta{padding:18px 20px 20px}
.card__top{display:flex;align-items:baseline;gap:9px;margin-bottom:7px}
.card__no{font-family:var(--mono);font-size:10px;color:var(--ink-3);letter-spacing:.08em}
.card__name{font-size:16px;font-weight:600;letter-spacing:-.01em}
.card__cn{font-size:12px;color:var(--ink-3)}
.card__desc{margin:0;font-size:12.5px;line-height:1.75;color:var(--ink-2);min-height:44px}
.card__tags{display:flex;flex-wrap:wrap;gap:6px;margin-top:12px}
.tag{font-size:10.5px;color:#9aa7c2;background:rgba(138,163,255,.08);
  border:1px solid rgba(138,163,255,.16);border-radius:4px;padding:2px 7px}
.tag.hot{color:#ffd9a8;background:rgba(255,190,110,.09);border-color:rgba(255,190,110,.22)}

.foot{position:relative;z-index:2;max-width:1240px;margin:0 auto;padding:6px 32px 72px;
  display:flex;flex-wrap:wrap;gap:14px;align-items:center;justify-content:space-between;
  border-top:1px solid var(--line);margin-top:18px;padding-top:24px}
.foot p{margin:0;font-size:12.5px;color:var(--ink-2)}
.foot p b{color:var(--ink);font-weight:500}
.foot code{font-family:var(--mono);font-size:11px;color:#9aa7c2;background:rgba(255,255,255,.04);
  border:1px solid var(--line);border-radius:4px;padding:2px 6px}

/* 全屏查看 */
.view{position:fixed;inset:0;z-index:100;display:none;background:#07080b}
.view.on{display:block}
.view__bar{position:absolute;top:0;left:0;right:0;z-index:3;height:52px;display:flex;align-items:center;gap:14px;
  padding:0 16px 0 20px;background:rgba(10,11,14,.86);backdrop-filter:blur(14px);
  border-bottom:1px solid var(--line);font-size:12px;color:var(--ink-2)}
.view__no{font-family:var(--mono);font-size:10px;color:var(--ink-3)}
.view__name{color:var(--ink);font-weight:600;font-size:13px}
.view__cn{color:var(--ink-3)}
.view__spacer{flex:1}
.view__btn{height:30px;padding:0 12px;border-radius:7px;border:1px solid var(--line);
  font-size:11.5px;color:var(--ink-2);display:inline-flex;align-items:center;gap:6px;transition:background .16s,color .16s}
.view__btn:hover{background:rgba(255,255,255,.06);color:var(--ink)}
.view__stage{position:absolute;inset:52px 0 0}
.view iframe{width:100%;height:100%;border:0;background:#07080b}

@media (max-width:900px){
  .head{padding:40px 20px 26px}
  .grid{grid-template-columns:minmax(0,1fr);padding:8px 20px 32px;gap:18px}
  .foot{padding:20px 20px 60px;flex-direction:column;align-items:flex-start}
  .view__bar{gap:10px}
  .view__cn{display:none}
}
@media (prefers-reduced-motion:reduce){
  *,*::before,*::after{animation-duration:.001ms!important;transition-duration:.001ms!important}
}
</style>
</head>
<body>

<header class="head">
  <span class="kicker"><i></i>APIDUO · LOGIN PAGE · 6 DIRECTIONS</span>
  <h1>登录页，六种风格</h1>
  <p>同一套内容、同一套字段，只换视觉语言。<b>点任意一张进入全屏</b>，方向键左右切换，Esc 退出。
     每套都做了桌面与移动两档，可直接当实现参照。</p>
  <div class="head__row">
    <span class="chip">6 套</span>
    <span class="chip">桌面 1440 / 移动 390</span>
    <span class="chip">单文件可交互</span>
    <span class="chip">无外部依赖</span>
  </div>
</header>

<main class="grid" id="grid"></main>

<footer class="foot">
  <p>如果只留一个：<b>Aurora 做深色默认</b>，<b>Light 做浅色主题配套</b>；两套共用同一份结构，只换 token。</p>
  <p><code>docs/apiduo/login-styles/</code> 下每套都是独立文件，可单独打开评审。</p>
</footer>

<div class="view" id="view" aria-hidden="true">
  <div class="view__bar">
    <span class="view__no" id="vNo"></span>
    <span class="view__name" id="vName"></span>
    <span class="view__cn" id="vCn"></span>
    <span class="view__spacer"></span>
    <button class="view__btn" id="vPrev">← 上一套</button>
    <button class="view__btn" id="vNext">下一套 →</button>
    <button class="view__btn" id="vClose">关闭 ✕</button>
  </div>
  <div class="view__stage"><iframe id="big" title="风格预览"></iframe></div>
</div>

<script>
const DATA = __DATA__;
const META = DATA.meta, DOCS = DATA.docs;
const FROZEN = s => s.replace('</head>', '<style>*,*::before,*::after{animation:none!important;transition:none!important;}</style></head>');

const grid = document.getElementById('grid');
META.forEach((m, i) => {
  const el = document.createElement('article');
  el.className = 'card';
  el.tabIndex = 0;
  el.setAttribute('role', 'button');
  el.setAttribute('aria-label', m.name + ' 风格预览');
  el.innerHTML =
    '<div class="card__shot"><iframe loading="lazy" title="' + m.name + '"></iframe></div>' +
    '<div class="card__meta">' +
      '<div class="card__top"><span class="card__no">' + m.no + '</span>' +
        '<span class="card__name">' + m.name + '</span>' +
        '<span class="card__cn">' + m.cn + '</span></div>' +
      '<p class="card__desc">' + m.desc + '</p>' +
      '<div class="card__tags">' + m.tags.map((t, j) =>
        '<span class="tag' + (j === m.tags.length - 1 && m.tags[j] === '主推' ? ' hot' : '') + '">' + t + '</span>').join('') +
      '</div>' +
    '</div>';
  el.addEventListener('click', () => open(i));
  el.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(i); } });
  grid.appendChild(el);
});

// 按卡片实际宽度缩放 1440 宽的预览
const cards = [...document.querySelectorAll('.card')];
function fit() {
  cards.forEach((c, i) => {
    const shot = c.querySelector('.card__shot');
    const fr = shot.querySelector('iframe');
    const s = shot.clientWidth / 1440;
    fr.style.transform = 'scale(' + s + ')';
    if (!fr.dataset.done) {
      fr.dataset.done = '1';
      fr.srcdoc = FROZEN(DOCS[META[i].id]);
    }
  });
}
fit();
addEventListener('resize', fit);

// 全屏
const view = document.getElementById('view');
const big = document.getElementById('big');
let cur = -1;

function open(i) {
  cur = (i + META.length) % META.length;
  const m = META[cur];
  document.getElementById('vNo').textContent = m.no;
  document.getElementById('vName').textContent = m.name;
  document.getElementById('vCn').textContent = m.cn;
  big.srcdoc = DOCS[m.id];
  view.classList.add('on');
  view.setAttribute('aria-hidden', 'false');
  document.body.style.overflow = 'hidden';
  document.getElementById('vClose').focus({ preventScroll: true });
}
function close() {
  view.classList.remove('on');
  view.setAttribute('aria-hidden', 'true');
  document.body.style.overflow = '';
  big.srcdoc = '';
  if (cur >= 0) cards[cur].focus({ preventScroll: true });
}
document.getElementById('vClose').addEventListener('click', close);
document.getElementById('vPrev').addEventListener('click', () => open(cur - 1));
document.getElementById('vNext').addEventListener('click', () => open(cur + 1));
addEventListener('keydown', e => {
  if (!view.classList.contains('on')) return;
  if (e.key === 'Escape') close();
  if (e.key === 'ArrowLeft') open(cur - 1);
  if (e.key === 'ArrowRight') open(cur + 1);
});
</script>
</body>
</html>
"""

out = TEMPLATE.replace("__DATA__", data)
(ROOT / "login-styles.html").write_text(out, encoding="utf-8")
print("written", len(out), "bytes")

# ── 第二份产物：后台「登录页风格」配置项的原型 ───────────────────────────────
CONFIG = r"""<!DOCTYPE html>
<!--
  apiduo 后台配置原型：系统设置 → 登录页风格
  仅用于评审，不参与构建。
  这一页就是「动态可配置」落地后的样子：一个 option（LoginTheme）+ 一套皮肤切换。
-->
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>Apiduo · 登录页风格配置（原型）</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root{
  --bg:#0a0b0f;--panel:#111319;--panel-2:#0d0f14;--ink:#e9edf5;--ink-2:#98a2b5;--ink-3:#6b7488;
  --line:rgba(255,255,255,.08);--line-2:rgba(255,255,255,.14);
  --accent:#8aa3ff;--mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
}
html,body{height:100%}
body{margin:0;background:var(--bg);color:var(--ink);font-size:14px;
  font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text","PingFang SC","Noto Sans SC",sans-serif;
  -webkit-font-smoothing:antialiased;overflow:hidden}
a{color:inherit;text-decoration:none}
button{font:inherit;color:inherit;background:none;border:0;cursor:pointer}
:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:6px}

.top{height:56px;display:flex;align-items:center;gap:12px;padding:0 20px;border-bottom:1px solid var(--line)}
.top__crumb{font-size:12px;color:var(--ink-3)}
.top__crumb b{color:var(--ink);font-weight:500}
.top__sp{flex:1}
.badge{font-family:var(--mono);font-size:9.5px;letter-spacing:.1em;color:#ffd9a8;
  border:1px solid rgba(255,190,110,.28);background:rgba(255,190,110,.08);border-radius:4px;padding:3px 7px}

.body{display:grid;grid-template-columns:312px minmax(0,1fr);height:calc(100vh - 56px - 62px)}

/* 左：风格列表 */
.list{border-right:1px solid var(--line);overflow-y:auto;padding:14px}
.list__h{font-size:11.5px;color:var(--ink-3);margin:2px 4px 12px;line-height:1.7}
.item{display:flex;align-items:center;gap:12px;width:100%;text-align:left;padding:8px;border-radius:12px;
  border:1px solid transparent;transition:background .16s,border-color .16s}
.item:hover{background:rgba(255,255,255,.035)}
.item.on{background:rgba(138,163,255,.10);border-color:rgba(138,163,255,.32)}
.item__shot{width:74px;height:50px;border-radius:7px;overflow:hidden;flex:none;position:relative;
  border:1px solid rgba(255,255,255,.10)}
.item__meta{min-width:0;flex:1}
.item__name{font-size:13px;font-weight:600;letter-spacing:-.005em;display:flex;align-items:center;gap:6px}
.item__cn{font-size:11px;color:var(--ink-3);margin-top:1px}
.dot{width:13px;height:13px;border-radius:50%;border:1px solid var(--line-2);flex:none;display:grid;place-items:center}
.item.on .dot{border-color:var(--accent);background:var(--accent)}
.item.on .dot::after{content:'';width:5px;height:5px;border-radius:50%;background:#0a0b0f}

/* 缩略图：纯 CSS 画的极简版，不加载页面 */
.th{position:absolute;inset:0}
.th__card{position:absolute;border-radius:3px}
.th i{position:absolute;left:9%;display:block;border-radius:2px}

/* 右：预览 */
.preview{display:flex;flex-direction:column;min-width:0}
.pv__bar{height:40px;display:flex;align-items:center;gap:10px;padding:0 16px;border-bottom:1px solid var(--line);
  font-size:11.5px;color:var(--ink-3);flex:none}
.pv__bar b{color:var(--ink-2);font-weight:500}
.pv__stage{flex:1;position:relative;overflow:hidden;background:#07080b}
.pv__stage iframe{position:absolute;top:0;left:0;width:1440px;height:900px;border:0;transform-origin:top left}

/* 底部保存条 */
.save{height:62px;display:flex;align-items:center;gap:14px;padding:0 20px;border-top:1px solid var(--line);
  background:var(--panel-2)}
.save__code{font-family:var(--mono);font-size:11px;color:#9aa7c2;background:rgba(255,255,255,.04);
  border:1px solid var(--line);border-radius:6px;padding:6px 10px;white-space:nowrap;overflow:hidden;
  text-overflow:ellipsis;max-width:46vw}
.save__code b{color:#a9bcff;font-weight:400}
.save__sp{flex:1}
.btn{height:34px;padding:0 15px;border-radius:8px;font-size:12.5px;border:1px solid var(--line-2);color:var(--ink-2);
  transition:background .16s,color .16s,border-color .16s}
.btn:hover{background:rgba(255,255,255,.06);color:var(--ink)}
.btn--primary{background:linear-gradient(96deg,#3b62f6,#7250f5);border-color:transparent;color:#fff;font-weight:500;
  box-shadow:0 8px 20px -12px rgba(86,100,255,.9)}
.btn--primary:hover{filter:brightness(1.06);background:linear-gradient(96deg,#3b62f6,#7250f5);color:#fff}
.save__hint{font-size:11.5px;color:#7f8da5;transition:color .2s}
.save__hint.ok{color:#6ee7a8}

@media (max-width:900px){
  .body{grid-template-columns:minmax(0,1fr);grid-template-rows:auto 1fr}
  .list{border-right:0;border-bottom:1px solid var(--line);display:flex;gap:8px;overflow-x:auto;padding:10px}
  .list__h{display:none}
  .item{min-width:210px}
  .save__code{display:none}
}
</style>
</head>
<body>

<div class="top">
  <span class="top__crumb">系统设置 / <b>登录页风格</b></span>
  <span class="badge">PROTOTYPE</span>
  <span class="top__sp"></span>
  <span class="top__crumb" id="curTop">当前生效：Aurora</span>
</div>

<div class="body">
  <aside class="list" id="list">
    <p class="list__h">选择访客打开 <code>/sign-in</code> 时看到的皮肤。六套都保留在代码里，切换不重新构建。</p>
  </aside>

  <section class="preview">
    <div class="pv__bar"><span>预览</span><b id="pvName">Aurora · 深色极光</b><span id="pvSize">1440 × 900</span></div>
    <div class="pv__stage" id="stage"><iframe id="pv" title="风格预览"></iframe></div>
  </section>
</div>

<div class="save">
  <span class="save__code">PUT /api/option {"key":"<b>LoginTheme</b>","value":"<b id="codeVal">aurora</b>"}</span>
  <span class="save__hint" id="hint">未保存</span>
  <span class="save__sp"></span>
  <button class="btn" id="reset">恢复默认</button>
  <button class="btn btn--primary" id="save">保存配置</button>
</div>

<script>
const DATA = __DATA__;
const META = DATA.meta, DOCS = DATA.docs;
const KEY = 'apiduo:login-theme:preview';

/* 六套的缩略图配色（纯 CSS 画，不加载页面） */
const TH = {
  aurora:   {bg:'linear-gradient(135deg,#0b1020,#101a2e 55%,#1a1030)', card:'#161b27', cardB:'rgba(255,255,255,.14)', a:'#5b7cff', b:'#c9d4ea', w:'62%'},
  light:    {bg:'linear-gradient(160deg,#f7f9fc,#eef1f7)', card:'#ffffff', cardB:'rgba(15,23,42,.10)', a:'#2f5bff', b:'#0b1220', w:'62%'},
  terminal: {bg:'#060b09', card:'#0a100d', cardB:'rgba(92,242,160,.30)', a:'#5cf2a0', b:'#cfe6d8', w:'66%'},
  editorial:{bg:'#f2ede3', card:'transparent', cardB:'rgba(23,20,15,.18)', a:'#a8341b', b:'#17140f', w:'58%'},
  cyber:    {bg:'linear-gradient(180deg,#0a0416 55%,#1a0630)', card:'rgba(16,8,28,.92)', cardB:'rgba(255,45,149,.55)', a:'#ff2d95', b:'#ece7fb', w:'62%'},
  warm:     {bg:'linear-gradient(150deg,#f8f3ea,#f1e9dc)', card:'#fffdf8', cardB:'rgba(120,100,80,.14)', a:'#1d3b32', b:'#22302b', w:'62%'}
};

const list = document.getElementById('list');
const pv = document.getElementById('pv');
const stage = document.getElementById('stage');
let cur = 'aurora';

META.forEach(m => {
  const t = TH[m.id] || TH.aurora;
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'item';
  b.dataset.id = m.id;
  b.innerHTML =
    '<span class="item__shot" style="background:' + t.bg + '">' +
      '<span class="th">' +
        '<span class="th__card" style="right:8%;top:16%;width:' + t.w + ';bottom:16%;background:' + t.card + ';border:1px solid ' + t.cardB + '"></span>' +
        '<i style="top:26%;width:22%;height:4px;background:' + t.b + ';opacity:.85;border-radius:1px;left:auto;right:calc(8% + ' + t.w + ' - 26%)"></i>' +
        '<i style="top:52%;width:32%;height:5px;background:' + t.a + ';border-radius:2px;left:6%"></i>' +
      '</span>' +
    '</span>' +
    '<span class="item__meta"><span class="item__name">' + m.name + '</span>' +
    '<span class="item__cn">' + m.cn + '</span></span>' +
    '<span class="dot"></span>';
  b.addEventListener('click', () => pick(m.id));
  list.appendChild(b);
});

function fit() {
  const w = stage.clientWidth, h = stage.clientHeight;
  const s = Math.min(w / 1440, h / 900);
  pv.style.transform = 'scale(' + s + ')';
  pv.style.left = Math.max(0, (w - 1440 * s) / 2) + 'px';
  pv.style.top = Math.max(0, (h - 900 * s) / 2) + 'px';
}

function pick(id) {
  cur = id;
  const m = META.find(x => x.id === id) || META[0];
  pv.srcdoc = (DOCS[m.id] || '').replace('</head>',
    '<style>*,*::before,*::after{animation:none!important;transition:none!important}.markbar{display:none!important}</style></head>');
  document.getElementById('pvName').textContent = m.name + ' · ' + m.cn;
  document.getElementById('curTop').textContent = '当前生效：' + m.name;
  document.getElementById('codeVal').textContent = m.id;
  document.querySelectorAll('.item').forEach(el => el.classList.toggle('on', el.dataset.id === m.id));
  document.getElementById('hint').textContent = '已选择，未保存';
  document.getElementById('hint').classList.remove('ok');
  fit();
}

document.getElementById('save').addEventListener('click', () => {
  localStorage.setItem(KEY, cur);
  const h = document.getElementById('hint');
  h.textContent = '已保存（原型写 localStorage，真实环境写 option）';
  h.classList.add('ok');
});
document.getElementById('reset').addEventListener('click', () => {
  localStorage.removeItem(KEY);
  pick('aurora');
  const h = document.getElementById('hint');
  h.textContent = '已恢复默认 Aurora';
  h.classList.add('ok');
});

new ResizeObserver(fit).observe(stage);
addEventListener('resize', fit);
pick(localStorage.getItem(KEY) || 'aurora');
</script>
</body>
</html>
"""
cfg = CONFIG.replace("__DATA__", data)
(ROOT / "login-theme-config.html").write_text(cfg, encoding="utf-8")
print("written config", len(cfg), "bytes")
