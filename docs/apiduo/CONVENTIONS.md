# apiduo 项目约定

本文件记录**上游代码形态不支持自动校验**、只能靠人遵守的约定。能写成测试的规则不放这里。

---

## 新增后台管理页：三处联动

后台管理没有统一收口。权限判定分散在导航配置与每个管理路由各自的 `beforeLoad` 里，
两处都不具备「漏写即报错」的能力——页面照常渲染，只有接口 403 会暴露。

新增一个管理页必须同时改三处：

| # | 位置 | 内容 |
|---|---|---|
| 1 | `web/src/hooks/use-sidebar-data.ts` | 条目放进 `id: 'admin'` 分组的 `items`，填 `requiredRole`（`ROLE.ADMIN` 或 `ROLE.SUPER_ADMIN`） |
| 2 | `web/src/routes/_authenticated/<segment>/index.tsx`（或 `route.tsx` 作布局级守卫） | `beforeLoad` 里判角色，不满足则 `throw redirect({ to: '/403' })` |
| 3 | `web/src/i18n/locales/*.json` | 七个语言文件补导航标题 key |

### 校验覆盖到哪一步

`web/src/routes/__tests__/admin-guard.test.ts` 以**导航配置为唯一事实源**，反查每个
admin 条目对应的路由根是否仍带角色判断。

- 漏第 2 步 → 测试失败，CI 拦截。
- 漏第 3 步 → `bun run i18n:sync` 会提示缺失 key。
- **漏第 1 步 → 无法被发现。** 页面没进 admin 分组，导航里没有入口，测试也不会检查它。
  这是当前形态的固有盲区，只能靠这条约定。

### 判据写法

新页面统一用范围判断 `role < ROLE.X`，不要用严格判等 `role !== ROLE.X`。
后者在新增角色层级时会把新角色挡在页面外（过度拒绝），届时需要逐页修改；
前者只需要新角色的枚举值大于阈值即可自动通过。

现存 4 个页面用的是 `!== ROLE.SUPER_ADMIN`（`system-info` / `task-plugins` /
`request-processing` / `system-settings`），且这四处的守卫是上游原有代码。
**不做批量改写**——为写法一致性去动 9 个上游文件，冲突成本高于收益。
等到确实需要新增角色层级、或上游同步触发这些文件冲突时，再顺手统一。

---

## 相关文档

- `docs/apiduo/module-isolation-plan.md` — 上游同步的成本模型、挂接点分档与预算规则
- `.apiduo/merge-budget.txt` — 合并面预算基线，由 `go run ./tools/markercheck -cost` 校验
