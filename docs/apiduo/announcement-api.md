# 公告管理 API

面向外部 agent（codex、Claude Code 等）与脚本的公告 CRUD 接口。

**不要用 `PUT /api/option` 发公告。** 那是整包覆盖：调用方必须先读全量、
改数组、再写回，两个并发请求会互相覆盖，而且需要 `option:write` 权限。

---

## 鉴权

沿用现有的 personal access token（PAT）体系，与浏览器会话互相独立。

1. 由 **root 用户**在「安全 → 访问令牌」（`/security`）里创建一个 PAT
2. scope 只勾 **Announcements** 的 `Read` / `Write`
3. 拿到 `nap_` 开头的明文，**只显示一次**

```bash
TOKEN=nap_xxxxxxxx
```

权限边界是 scope 决定的，不是账号角色。一个只勾了 Announcements 的 PAT
调 `PUT /api/option/` 会得到 `403 lacks the required permission: option:write`。

> 创建 PAT 会触发二次验证（`SECURITY_PROOF_REQUIRED`），必须在浏览器里由人完成。
> agent 无法自助获取权限，这是设计如此。

---

## 接口

| 方法 | 路径 | scope |
|---|---|---|
| `GET` | `/api/announcements` | `announcement:read` |
| `POST` | `/api/announcements` | `announcement:write` |
| `PATCH` | `/api/announcements/:id` | `announcement:write` |
| `DELETE` | `/api/announcements/:id` | `announcement:write` |

`GET` 返回**全部**公告，包括尚未到发布时间与已经过期的。
这是管理视角——读者侧会过滤，但管理员必须能看到过期公告，否则改不了也删不掉。

`POST` 的 `id` 由服务端分配，请求体里带 `id` 会被忽略。

`PATCH` 只覆盖请求体里出现的字段，其余保持不变。

---

## 字段

```jsonc
{
  "id": 6,                                    // 服务端分配，只读
  "title": "11/20 维护",                       // 可选，≤60 字
  "content": "02:00–04:00 计划维护。",          // 必填，≤500 字，支持 Markdown/HTML
  "publishDate": "2026-11-20T02:00:00+08:00", // 必填，RFC3339
  "expiresAt": "2026-11-25T00:00:00+08:00",   // 可选，RFC3339，必须晚于 publishDate
  "type": "warning",                          // 可选：default|ongoing|success|warning|error
  "kind": "feature",                          // 仅 category=changelog：feature|improvement|fix
  "extra": "补偿方案见工单",                    // 可选，≤100 字
  "phases": [                                 // 可选，最多 8 个
    {
      "at": "2026-11-20T02:00:00+08:00",      // 必填，RFC3339
      "title": "开始维护",                     // 必填，≤60 字
      "desc": "边缘节点滚动重启"                // 可选，≤200 字
    }
  ]
}
```

空字符串等于不设置：`title` / `expiresAt` / `extra` 传空串会被删除，
而不是存一个空值。`title` 为空时，列表回落显示正文摘要。

### category 与 kind

`category` 决定这条消息出现在哪里：

| 值 | 展示位 | 用得到的字段 |
|---|---|---|
| `announcement`（缺省） | 仪表盘公告面板 + 铃铛未读 | `type`、`expiresAt`、`phases` |
| `changelog` | 页面顶部功能更新横幅，看过即消失 | `kind` |

`kind` 是变更性质（新增 / 优化 / 修复），与公告的 `type`（严重程度）是**两个维度**，
所以没有复用同一个字段 —— 否则会出现「带警告级别的功能更新」这种语义错乱。

两者都可以不填。`kind` 不填就不显示标签，不会默认成 `feature`。

### 时区

三个时间字段都是带偏移的 RFC3339。**中国部署请写 `+08:00`**，不要写 `Z`：

```jsonc
"publishDate": "2026-11-16T10:00:00+08:00"   // 好：读库时一眼就是本地时间
"publishDate": "2026-11-16T02:00:00Z"        // 也是同一时刻，但读的时候要心算八小时
```

两者语义等价，服务端和前端都接受。这里要求 `+08:00` 只是为了让人读库时不用换算 ——
后台编辑器产出的也是这个格式。

---

## 读者侧怎么看到它

`GET /api/status` 的 `announcements` 字段按 `[publishDate, expiresAt)` 过滤：

- `publishDate` 在未来 → 不显示（到点自动出现）
- `expiresAt` 已过 → 不显示（到点自动消失）
- 时间字段缺失或格式非法 → **照常显示**（宁可多显示一条，也不能因为一个错字让有效公告消失）

这两个边界不需要任何定时任务，每次请求现算。

---

## 示例

### 发一条带时间线的维护公告

```bash
curl -X POST http://your-host/api/announcements \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "11/20 维护",
    "content": "02:00–04:00 计划维护，期间请求可能重试。",
    "publishDate": "2026-11-17T10:00:00+08:00",
    "expiresAt": "2026-11-21T00:00:00+08:00",
    "type": "warning",
    "phases": [
      {"at": "2026-11-20T02:00:00+08:00", "title": "开始维护", "desc": "边缘节点滚动重启"},
      {"at": "2026-11-20T03:00:00+08:00", "title": "核心切换", "desc": "主库切换"},
      {"at": "2026-11-20T04:00:00+08:00", "title": "恢复服务", "desc": "全量回归通过"}
    ]
  }'
```

### 改一条（只传要改的）

```bash
curl -X PATCH http://your-host/api/announcements/6 \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title": "11/20 维护（时间调整）"}'
```

### 删除

```bash
curl -X DELETE http://your-host/api/announcements/6 \
  -H "Authorization: Bearer $TOKEN"
```

---

## 错误

业务错误返回 **HTTP 200 + `success: false`**，不是 4xx —— 这是网关的既有约定。

```json
{"success": false, "message": "Announcement #6 expiry must be later than its publish date"}
```

例外是「资源不存在」，返回 HTTP 404：

```json
{"success": false, "message": "Announcement not found"}
```

常见拒绝：

| 消息 | 原因 |
|---|---|
| `expiry must be later than its publish date` | 一发布就过期，是配置错误 |
| `title cannot exceed 60 characters` | 标题超长 |
| `An announcement cannot have more than 8 phases` | 阶段超过 8 个 |
| `phase #N is missing the at field` | 阶段缺时间 |
| `The access token lacks the required permission: announcement:write` | scope 没勾 |

---

## 前端与它是同一份数据

管理后台「系统设置 → 内容 → 公告」读写的是同一个 `console_setting.announcements`
JSON 数组。agent 建的公告在后台可以直接编辑，反之亦然。

后台保存走的是 `/api/option` 整包覆盖，仍然存在并发覆盖的可能；
agent 走这里的按 id 接口没有这个问题。**两边同时编辑时以 agent 为准更安全。**
