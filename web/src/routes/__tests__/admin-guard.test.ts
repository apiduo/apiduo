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
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

/**
 * 管理页的角色守卫回归。
 *
 * 上游把守卫写在每个管理路由自己的 `beforeLoad` 里，形状是「N 个文件各自判断」，
 * 没有单一事实源。上游同步时删掉一处守卫、或我们新增管理页时忘记写，都不会有
 * 任何机制发现——页面照常渲染，只有接口 403 会暴露。
 *
 * 这份测试不改变守卫形状，只加一个哨兵：**以导航配置为事实源**，反查每个管理域
 * 的路由根是否仍带角色判断。新增管理页只要进 admin 分组，就自动被覆盖，不需要
 * 维护第二份清单。
 *
 * 判据范围：只证明「守卫还在」，不约束比较方向（`< ROLE.ADMIN` 与
 * `!== ROLE.SUPER_ADMIN` 都算数）。两种写法的一致性属于独立议题。
 */

const here = path.dirname(fileURLToPath(import.meta.url))
const srcRoot = path.resolve(here, '../..')
const routesRoot = path.join(srcRoot, 'routes/_authenticated')

/** 路由前置钩子 + 引用角色枚举，即认为该路由仍在做角色判断。 */
function hasRoleGuard(source: string): boolean {
  return source.includes('beforeLoad') && /ROLE\.[A-Z_]+/.test(source)
}

/** 截取 `items: [` 起配对的方括号内容，避免把后面的分组算进来。 */
function sliceItemsLiteral(source: string, searchFrom: number): string {
  const start = source.indexOf('items: [', searchFrom)
  if (start === -1) throw new Error('items array not found after admin group')
  const open = source.indexOf('[', start)
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '[') depth++
    else if (source[i] === ']') {
      depth--
      if (depth === 0) return source.slice(open, i + 1)
    }
  }
  throw new Error('unbalanced items array')
}

/** admin 分组的 url 列表，取自导航数据而不是另抄一份清单。 */
function adminNavUrls(): string[] {
  const file = fs.readFileSync(
    path.join(srcRoot, 'hooks/use-sidebar-data.ts'),
    'utf8'
  )
  const anchor = file.indexOf("id: 'admin'")
  if (anchor === -1) {
    throw new Error('admin group not found in hooks/use-sidebar-data.ts')
  }
  return [...sliceItemsLiteral(file, anchor).matchAll(/url:\s*'([^']+)'/g)]
    .map((match) => match[1])
    .filter((url): url is string => Boolean(url))
}

/** 该 url 所属管理域的路由根；`route.tsx` 是布局级守卫，优先于 `index.tsx`。 */
function routeRootFor(url: string): string | null {
  const segment = url.split('/').find(Boolean)
  if (!segment) return null
  const candidates = [
    path.join(routesRoot, segment, 'route.tsx'),
    path.join(routesRoot, segment, 'index.tsx'),
    path.join(routesRoot, `${segment}.tsx`),
  ]
  return candidates.find((candidate) => fs.existsSync(candidate)) ?? null
}

const adminUrls = adminNavUrls()

describe('admin route guards', () => {
  it('reads the admin group from the navigation data', () => {
    // 空数组会让下面每个断言空转，必须单独守住。
    expect(adminUrls.length).toBeGreaterThan(0)
  })

  it.each(adminUrls)('%s keeps a role guard on its route root', (url) => {
    const routeFile = routeRootFor(url)
    if (!routeFile) {
      throw new Error(`no route root found for admin entry ${url}`)
    }
    expect(
      hasRoleGuard(fs.readFileSync(routeFile, 'utf8')),
      `${url} lost its role guard (${path.relative(srcRoot, routeFile)})`
    ).toBe(true)
  })

  it('keeps the admin group hidden from non-admins in the sidebar', () => {
    // 导航层兜底：分组过滤失效时，普通用户会在侧边栏看到管理入口。
    const source = fs.readFileSync(
      path.join(srcRoot, 'hooks/use-sidebar-view.ts'),
      'utf8'
    )
    expect(source).toContain("group.id === 'admin'")
  })
})
