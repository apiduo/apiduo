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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { Main } from '@/components/layout'
/* APIDUO:playground-shell:BEGIN */
// apiduo 的 Playground：三模式（对话 / 生图 / 生视频）+ 每次请求的计费对账。
// 上游实现保留在 features/playground 不动，这里只换入口。
import { ApiduoPlayground } from '@/features/apiduo-playground'
/* APIDUO:playground-shell:END */
import { isSidebarModuleEnabled } from '@/lib/nav-modules'

export const Route = createFileRoute('/_authenticated/playground/')({
  beforeLoad: () => {
    if (!isSidebarModuleEnabled('chat', 'playground')) {
      throw redirect({ to: '/dashboard' })
    }
  },
  component: PlaygroundPage,
})

function PlaygroundPage() {
  return (
    <Main className='p-0'>
      {/* APIDUO:playground-shell:BEGIN */}
      <ApiduoPlayground />
      {/* APIDUO:playground-shell:END */}
    </Main>
  )
}
