# 与上游同步

本仓是 `QuantumNous/new-api` 的 fork。目标不是「永不冲突」——那不现实——而是让每次冲突都能在十分钟内解开，且不会因为解错而引入静默的行为变化。

## 判据

只看一件事：**这一行是我们写的，还是上游写的？**

如果合并时无法一眼判断，冲突就只能靠记忆去拼。所以约定是把自有代码包进标记块：

```go
// APIDUO:artifact-rewrite:BEGIN
if rewritten, changed := service.RewriteResponseArtifacts(...); changed { ... }
// APIDUO:artifact-rewrite:END
```

```tsx
{/* APIDUO:file-relay:BEGIN */}
<FileRelaySetting channelType={currentType} />
{/* APIDUO:file-relay:END */}
```

`<feature>` 用功能名，全仓统一，不要为同一个功能另起第二个名字。

三条硬规则：

1. **上游已有的文件里，新增行必须落在标记块内**
2. **不改上游既有行的内容**（需要改时尽量改成「新增 + 条件包裹」，实在要改就在提交信息里说明）
3. **不重命名、不删除上游文件**

新增文件不受约束——上游永远不会碰到它们。

## 检查

```bash
go run ./tools/markercheck          # 有新增违规退出码 1
go run ./tools/markercheck -v       # 附带每个文件删除上游行的数量
go run ./tools/markercheck -update  # 按实际情况重写遗留基线
go test ./tools/markercheck/        # 检查器自身的单测
```

输出是一张「新增 / 已标记 / 未标记」表，按未标记降序排列。

`ci` 里 `brand-release.yml` 会在构建镜像之前跑一次：**不合规就不发版**。

## 两个账本

**`.apiduo/sync-base`** —— 一行 SHA，指向**上次同步到的上游提交**（不是我们自己的 merge commit）。

它同时决定两件事：哪些文件算「上游的文件」（在这个提交里存在过才算），以及从哪个点开始算自有代码。用上游提交而不是 merge commit，是因为 merge commit 里已经包含了我方早先新增的文件——拿它当基线，会把自己的文件误判成上游的。

**`.apiduo/marker-baseline.txt`** —— 历史欠账，按文件记「还有多少行没标记」。

同步点之前的代码不追溯，但也不允许变多：某文件的实际未标记行数超过账本即失败，低于账本则提示可以收紧。等于给存量债务加了个只降不升的闸门。

## 同步上游的步骤

```bash
git fetch upstream
git merge upstream/main            # 不是 rebase：已在共享分支上，rebase 会重写历史
# 解冲突：标记块按「两边都留」处理，除非上游改了同一段语义

go build ./... && go test ./controller/ ./service/ ./setting/... ./pkg/blobstore/
cd web && bun run typecheck && bun run build && cd ..

git rev-parse upstream/main > .apiduo/sync-base   # 同步点前移
go run ./tools/markercheck -update                # 按新同步点重新记账
go run ./tools/markercheck                        # 确认通过

git add -A && git commit -m "chore(sync): 合并上游 $(git rev-parse --short upstream/main)"
```

`-update` 必须在 `sync-base` 之后跑：同步点前移意味着更多代码被划进「历史」，账本要跟着重算，否则数字对不上会误报。

排优先级时先看上游活跃度，别凭感觉：

```bash
for f in $(git diff --name-only upstream/main HEAD); do
  echo "$(git log --oneline --since='180 days ago' upstream/main -- "$f" | wc -l) $f"
done | sort -rn | head
```

上游半年改几十次的文件，一行自有代码都是成本；半年改一次的文件，几十行也无所谓。

## 已知遗留

历史遗留（同步点之前）尚未全部标记，明细在 `.apiduo/marker-baseline.txt`，也可以直接看检查输出。

截至 2026-11-07，约 446 行散在 36 个上游文件里，集中在：

| 文件 | 未标记 | 上游/180天 | 备注 |
|---|---|---|---|
| `web/src/features/dashboard/index.tsx` | 76 | 3 | 量大但上游几乎不动 |
| `web/src/features/dashboard/api.ts` | 70 | 1 | 同上 |
| `web/src/features/dashboard/types.ts` | 51 | 2 | 同上 |
| `model/user_access_token.go` | 50 | 3 | 令牌类型扩展 |
| `controller/access_token.go` | 22 | 6 | |
| `middleware/access_token_routes.go` | 11 | 3 | |

结论：**dashboard 那批虽然量大，风险最低**（上游一年改两三次）。真正该优先补的是上游活跃且未标记行多的文件。已经补完的高频文件：`channel-mutate-drawer.tsx`（上游 29 次/180天）、`channel-form.ts`（17 次）、`channels/types.ts`（14 次）。

## 放不下标记的地方

- **JSON**（`web/src/i18n/locales/*.json`、`web/package.json`）：没有注释语法。这类文件靠「新增键追加在末尾」减少冲突，不参与检查。
- **锁文件**（`go.sum`、`web/bun.lock`）：由工具生成，整块变动，标记没有意义。
- **生成文件**（`web/src/routeTree.gen.ts`）：手写的标记会被下次生成覆盖。

这三类在 `tools/markercheck` 里按后缀/文件名跳过，理由写在常量注释里。
