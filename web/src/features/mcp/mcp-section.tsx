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
import { useCallback, useEffect, useRef, useState } from 'react'

import { useTheme } from '@/context/theme-provider'
import { useAuthStore } from '@/stores/auth-store'

/**
 * MCP 能力注册面板。
 *
 * 面板本体是 `mcp/panel/index.html`，编译进二进制后由 `/mcp/admin` 直接输出。
 * 这里用 iframe 而不是重写一套 React：那份面板已经端到端验过
 * （声明、观察、巡检、错误规则），重写等于把同一套行为分叉成两份，
 * 以后每改一处都要对着两个地方核。
 *
 * 有两件事 iframe 自己做不到，必须由宿主补：
 *
 *   1. access token —— 面板独立打开时靠 `/api/user/auth/refresh` 换令牌，
 *      而那个接口与登录、agent 握手共用 CriticalRateLimit（30 次 / 1800s）。
 *      嵌进设置页后每进一次就烧掉一格额度，用户随后登录会被 429。
 *      宿主本来就持有 token，直接 postMessage 递进去。
 *
 *   2. 主题 —— iframe 里读不到宿主 `<html>` 上的 dark class，只能按
 *      `prefers-color-scheme` 猜。应用主题与操作系统不一致时（用户手动选过深色），
 *      嵌入的页面就会一边深一边浅。所以主题既写进 src（首屏不闪），
 *      也在切换时补发消息（不重载，避免丢掉面板里没保存的编辑）。
 */
export function McpSection() {
  const { resolvedTheme } = useTheme()
  const accessToken = useAuthStore((state) => state.auth.accessToken)
  const frameRef = useRef<HTMLIFrameElement | null>(null)
  // 只取首帧主题拼进 src；后续切换走 postMessage，改 src 会重载整个面板。
  const [initialTheme] = useState(resolvedTheme)

  const post = useCallback((message: Record<string, unknown>) => {
    frameRef.current?.contentWindow?.postMessage(message, window.location.origin)
  }, [])

  // 面板每次加载完都重新递一遍：它启动时手上没有任何令牌。
  const handleLoad = useCallback(() => {
    post({ type: 'apiduo:mcp-theme', theme: resolvedTheme })
    if (accessToken) post({ type: 'apiduo:mcp-token', token: accessToken })
  }, [post, resolvedTheme, accessToken])

  // 宿主侧令牌轮换后补发，否则面板还在用一小时前那枚。
  useEffect(() => {
    if (accessToken) post({ type: 'apiduo:mcp-token', token: accessToken })
  }, [post, accessToken])

  useEffect(() => {
    post({ type: 'apiduo:mcp-theme', theme: resolvedTheme })
  }, [post, resolvedTheme])

  return (
    <div className='flex h-full min-h-0 flex-col overflow-hidden rounded-lg border'>
      <iframe
        ref={frameRef}
        src={`/mcp/admin?embed=1&theme=${initialTheme}`}
        title='MCP'
        onLoad={handleLoad}
        className='h-full w-full flex-1 border-0'
      />
    </div>
  )
}
