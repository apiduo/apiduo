# 浏览器端 e2e

## 为什么存在

`src/**/__tests__` 跑在 jsdom 里，它能证明 `data-apiduo` 挂在了正确的元素上，但**证明不了浏览器把那个属性画成了可见的标记**。`background-clip: text` 在不支持的浏览器上会被静默丢弃，渐变是不是真的生效只有 `getComputedStyle` 说了算。这个目录只干这一件事。

## 怎么跑

```bash
cd web

# 先有一个已经初始化好的实例
E2E_BASE_URL=http://127.0.0.1:3000 \
E2E_PASSWORD='初始化时设的管理员密码' \
bun run test:e2e
```

失败时退出码为 1，截图落在 `web/e2e/output/`（已 gitignore）。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `E2E_BASE_URL` | `http://127.0.0.1:3000` | 目标实例，末尾斜杠会被去掉 |
| `E2E_USERNAME` | `root` | 管理员账号 |
| `E2E_PASSWORD` | 无，**必填** | 管理员密码 |
| `E2E_CHROME` | 自动探测 | 浏览器路径；依次找 macOS Chrome、`/usr/bin/google-chrome`、chromium |
| `E2E_OUTPUT` | `e2e/output` | 截图目录 |

## 为什么用 `puppeteer-core` 而不是 Playwright

`puppeteer-core` 不下载浏览器，直接借用本机已有的 Chrome。Playwright 要多下 100MB+ 的浏览器，而这个仓库目前只有一处需要真实浏览器，不值当。

## 为什么没接 CI

接 CI 要凑齐三件事：构建 Go 二进制（含 `web/dist` embed）、准备 Chrome、**初始化一个实例**（新版走 `/api/setup` 向导，不是自动建 root）。第三件事意味着 CI 里要写初始化脚本，而它是被测代码的一部分——用它来准备自己，覆盖率就不可信了。

等确实有人在 PR 上需要它时再接，届时优先考虑复用镜像而不是在 runner 上现装。

## 加新检查时

断言要读**计算样式**，不是读属性:

```ts
const style = getComputedStyle(element)
style.webkitBackgroundClip  // 'text' 才说明渐变裁剪生效
style.backgroundImage       // 含 'gradient' 才说明渐变存在
```

只断言 `data-apiduo` 存在等于重复单测，没有意义。
