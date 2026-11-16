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
import { CommandMenu } from '@/components/command-menu'
import { AnimatedOutlet } from '@/components/page-transition'
import { SkipToMain } from '@/components/skip-to-main'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import { LayoutProvider } from '@/context/layout-provider'
import { SearchProvider } from '@/context/search-provider'
import { getCookie } from '@/lib/cookies'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'
/* APIDUO:marks:BEGIN */
import { ApiduoMarkDefs } from '@/features/apiduo-marks/mark-defs'
/* APIDUO:marks:END */
/* APIDUO:appearance:BEGIN */
import { ApiduoAppearanceProvider } from '@/features/apiduo-appearance'
/* APIDUO:appearance:END */

import { AnnouncementBanner } from '@/components/announcement-banner'
import { AppHeader } from './app-header'
import { AppSidebar } from './app-sidebar'

type AuthenticatedLayoutProps = {
  children?: React.ReactNode
}

export function AuthenticatedLayout(props: AuthenticatedLayoutProps) {
  const defaultOpen = getCookie('sidebar_state') !== 'false'
  const signedIn = useAuthStore((s) => !!s.auth.user)
  // Unmount pages on sign-out, otherwise their queries refetch and toast "Session expired!" / "invalid access token"
  if (!signedIn) return null

  return (
    <LayoutProvider>
      <SearchProvider>
        <SidebarProvider defaultOpen={defaultOpen} className='flex-col'>
          {/* APIDUO:marks:BEGIN */}
          {/* Paint server for the mark-stroked icons; must live above every
              surface that references `url(#apiduo-mark-grad)`. */}
          <ApiduoMarkDefs />
          {/* APIDUO:marks:END */}
          <SkipToMain />
          <AppHeader />
          <div className='flex min-h-0 w-full flex-1'>
            <AppSidebar />
            <SidebarInset
              className={cn(
                '@container/content',
                'h-[calc(100svh-var(--app-header-height,0px))]',
                'min-h-0 overflow-hidden',
                'peer-data-[variant=inset]:h-[calc(100svh-var(--app-header-height,0px)-(var(--spacing)*4))]'
              )}
            >
              {/* APIDUO:appearance:BEGIN */}
              {/* Publishes the effect as :root variables, so a console edit
                  retunes every mounted apiduo surface without a re-render. */}
              <ApiduoAppearanceProvider>
                <div className='flex h-full min-h-0 flex-col'>
                  {/* 功能更新横幅：内容区顶部，不带 sticky —— 推内容而不是盖内容。
                      没有未读更新时组件返回 null，不占高度。 */}
                  <AnnouncementBanner />
                  <div className='min-h-0 flex-1'>
                    {props.children ?? <AnimatedOutlet />}
                  </div>
                </div>
              </ApiduoAppearanceProvider>
              {/* APIDUO:appearance:END */}
            </SidebarInset>
          </div>
        </SidebarProvider>
        <CommandMenu />
      </SearchProvider>
    </LayoutProvider>
  )
}
