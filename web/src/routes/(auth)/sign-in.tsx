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
import { z } from 'zod'

import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
// APIDUO:login-skin:BEGIN
import { ApiduoSignIn } from '@/features/apiduo-login'
// APIDUO:login-skin:END
import { resolveAuthentication } from '@/lib/auth-session'
// APIDUO:login-skin:BEGIN
import { hasSessionHint } from '@/lib/session-hint'
// APIDUO:login-skin:END
import { useAuthStore } from '@/stores/auth-store'

const searchSchema = z.object({
  redirect: z.string().optional(),
})

export const Route = createFileRoute('/(auth)/sign-in')({
  // APIDUO:login-skin:BEGIN
  // 上游的 SignIn 保留在 features/auth/sign-in，皮肤版只是换了外层壳；
  // 这里换掉 component 是唯一挂接点，别处一律落新文件。
  component: ApiduoSignIn,
  // APIDUO:login-skin:END
  validateSearch: searchSchema,
  beforeLoad: async ({ search }) => {
    // APIDUO:login-skin:BEGIN
    // 提示 Cookie 与 Refresh Cookie 在服务端同生同灭（service/auth_session.go）。
    // 没有提示就说明没有可换的会话：回源只会拿到 401，还会消耗与登录共享的
    // CRITICAL_RATE_LIMIT（20 次/20 分钟/IP），同一 NAT 出口的用户会被一起拖累。
    if (!hasSessionHint() && !useAuthStore.getState().auth.user) {
      return
    }
    // APIDUO:login-skin:END
    // 根 guard 可能因为没有会话提示而跳过了 refresh。此处必须回源确认，
    // 否则持有有效 Refresh Cookie 的用户会被要求重新输入密码。
    await resolveAuthentication()

    const { auth } = useAuthStore.getState()

    // 如果已经有用户信息，说明已登录
    if (auth.user) {
      const target =
        sanitizeAuthRedirect(search?.redirect, window.location.origin) ??
        '/dashboard'
      throw redirect({ href: target, replace: true })
    }
  },
})
