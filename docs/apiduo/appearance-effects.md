# 外观特效（appearance effects）

apiduo 新增的界面统一挂一层装饰性高光。它有两个作用：让这些界面看起来属于同一套，
也给「这个入口是 apiduo 加的」一个不依赖菜单位置的视觉证据。

实现在三处，互不交叉：

| 层 | 位置 | 职责 |
|---|---|---|
| 样式 | `web/src/styles/apiduo-appearance.css` | 画：五种效果，全部由 `--ap-*` 变量驱动 |
| 配置 | `setting/system_setting/apiduo_appearance.go` | 存：7 个扁平字段，写入 option 表 |
| 运行时 | `web/src/features/apiduo-appearance/` | 读：`GET /api/appearance` → 写 `:root` 变量 |

## 变量

Provider 把配置写在 `document.documentElement` 上，任何层直接读：

```
--ap-color   任意 CSS 颜色；不设置 = 跟随 --primary
--ap-speed   转一圈，如 3200ms
--ap-ring    光带粗细
--ap-tail    彗尾角度
```

`--ap-color` 在颜色为空时是**被删除**而不是置空：写成空串会让
`var(--ap-color, var(--primary))` 解析成空值、整条声明失效。这是 `var()` 的经典静默失败。

## 挂到一个新界面

容器加属性，里面放组件，两步：

```tsx
import { ApiduoGlow } from '@/features/apiduo-appearance'

<div data-ap-surface='' className='relative rounded-lg border p-3'>
  <ApiduoGlow />
  {/* 原有内容 */}
</div>
```

`data-ap-surface` 只保证 `position: relative`（供绝对定位的层用）和 spotlight 的指针坐标。
容器的 `border-radius` 会被层 `inherit`，所以圆角跟得上。

整体区块也可以直接用 `<ApiduoSurface>`，它把容器和层一起渲染。

**克制使用**：挂在卡片级容器上，不要逐行扫。整页都在扫等于整页都没在扫。

## 加一种风格

只改前端，后端不动 —— 服务端只存扁平参数，不认识风格 id。

1. `web/src/styles/apiduo-appearance.css` 加一条规则，例如：

```css
.ap-ripple {
  position: absolute; inset: 0; border-radius: inherit; pointer-events: none;
  animation: ap-ripple 2.4s ease-out infinite;
}
@keyframes ap-ripple { from { box-shadow: 0 0 0 0 …; } to { box-shadow: 0 0 0 12px transparent; } }
```

2. `web/src/features/apiduo-appearance/types.ts` 的 `APIDUO_EFFECT_STYLES` 加值
3. `components/apiduo-glow.tsx` 的 `switch` 加分支
4. `setting/system_setting/apiduo_appearance.go` 的 `ApiduoAppearanceStyles` 加同名常量

第 4 步是必须的：服务端有一份白名单，非法值会回落 `flow`。两份列表不一致时，
配置页能选但保存后不生效 —— 所以加风格时两边一起改。

## 加一个节假日预设

`presets.ts` 的 `APPEARANCE_PRESETS` 加一条即可，**不需要后端改动**：

```ts
{
  id: 'lantern',
  labelKey: 'Lantern Festival',   // 英文原文，页面用 t() 翻译
  hintKey: 'Warm gold',
  style: 'flow', color: '#f59e0b',
  speedMs: 2800, ringPx: 2, tailDeg: 140, glow: true,
}
```

预设只是「一键填表」，不是运行时绑定：选中后所有字段会被展开写进配置。
所以以后调整某个预设的参数，已经保存过的站点不会跟着变 —— 这是有意的，
管理员改过滑块之后，那些数字就是他自己的了。

## 约束

- **reduced-motion**：`prefers-reduced-motion: reduce` 下描边层和扫光层 `display: none`，
  呼吸环退化成静态 2px 边框。不是停动画，是不画。
- **颜色对比**：高光本质是「比边框更亮」。接近白或接近黑的颜色会在浅色或深色主题下消失，
  预设表里的颜色都是中间亮度，是有原因的。
- **overflow**：层用 `inset: 0` 而不是 `-1px`，因为卡片常常有 `overflow: hidden`，
  向外扩一圈会被裁成零。
- **性能**：柔光开关走 `filter: drop-shadow`，每个界面一次 filter pass。界面很多时可以关掉。
  扫光/描边都是合成层动画，不触发重排。
