/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

/**
 * 登录页皮肤注册表。
 *
 * 一个 id 同时是三处的东西：`status.login_theme` 的值、后台下拉的选项、
 * 以及根节点上的 `data-login-skin`。三者靠这张表保持同源，新增一套皮肤
 * 只需要在这里加一行 + 在 skins.css 里加一段，不需要改组件。
 */
export const LOGIN_SKIN_IDS = [
  'aurora',
  'light',
  'terminal',
  'editorial',
  'cyber',
  'warm',
] as const

export type LoginSkinId = (typeof LOGIN_SKIN_IDS)[number]

export const DEFAULT_LOGIN_SKIN: LoginSkinId = 'aurora'

/** 后台下拉用的名字。中文名是给人看的，英文名是给 id 对不上的时候看的。 */
export const LOGIN_SKIN_LABELS: Record<
  LoginSkinId,
  { name: string; cn: string; note: string }
> = {
  aurora: {
    name: 'Aurora',
    cn: '深色极光',
    note: '极光背景 + 玻璃卡，AI 产品感最强',
  },
  light: {
    name: 'Light',
    cn: '浅色精密',
    note: '白卡冷灰底，和浅色控制台无缝衔接',
  },
  terminal: {
    name: 'Terminal',
    cn: '终端',
    note: '等宽字体与命令行语气，面向开发者',
  },
  editorial: {
    name: 'Editorial',
    cn: '纸面编辑',
    note: '宋体标题与细线，B 端客户最稳重',
  },
  cyber: {
    name: 'Cyber',
    cn: '赛博网格',
    note: '透视地面与霓虹描边，冲击力最强',
  },
  warm: {
    name: 'Warm',
    cn: '暖调亲和',
    note: '米色与陶土橘，非技术访客更放松',
  },
}

export function isLoginSkinId(value: unknown): value is LoginSkinId {
  return (
    typeof value === 'string' &&
    (LOGIN_SKIN_IDS as readonly string[]).includes(value)
  )
}

/**
 * `?theme=` 只用于预览，不进路由的 search schema —— 那个 schema 属于上游文件，
 * 为一行预览参数去改它不划算。读不到就返回 null。
 */
export function readSkinOverride(): string | null {
  if (typeof window === 'undefined') return null
  try {
    return new URLSearchParams(window.location.search).get('theme')
  } catch {
    return null
  }
}

/**
 * 生效顺序：URL 覆盖（预览）> 后台配置 > 默认。
 *
 * 未知值一律回落成默认皮肤：一个拼错的 option 不该把登录页变成白屏，
 * 这是登录链路，宁可退回旧样子也不能不可用。
 */
export function resolveLoginSkin(
  configured: string | null | undefined,
  override: string | null = null
): LoginSkinId {
  if (isLoginSkinId(override)) return override
  if (isLoginSkinId(configured)) return configured
  return DEFAULT_LOGIN_SKIN
}
