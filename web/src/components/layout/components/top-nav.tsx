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
import { Link } from '@tanstack/react-router'
import { Menu } from 'lucide-react'
import { useMemo } from 'react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
/* APIDUO:dynamic-nav:BEGIN */
import { useNavAppOpener } from '@/features/nav-apps/hooks'
import { navAppUsesTemplate } from '@/features/nav-apps/lib/template'
/* APIDUO:dynamic-nav:END */
import { cn } from '@/lib/utils'

import { type TopNavLink } from '../types'

type TopNavProps = React.HTMLAttributes<HTMLElement> & {
  links: TopNavLink[]
}

/**
 * 顶部导航栏组件
 * 在大屏幕显示水平导航，在小屏幕显示下拉菜单
 */
export function TopNav({ className, links, ...props }: TopNavProps) {
  // APIDUO:dynamic-nav:BEGIN
  const openApp = useNavAppOpener()

  // A templated link cannot be followed as stored: its placeholders stand for
  // the viewer's key and this deployment's address, resolved at click time.
  const handleExternalClick = (event: React.MouseEvent, href: string) => {
    if (!navAppUsesTemplate(href)) return
    event.preventDefault()
    void openApp(href)
  }
  // APIDUO:dynamic-nav:END

  // 规范化链接，确保所有可选属性都有默认值
  const normalizedLinks = useMemo(
    () =>
      links.map((link) => ({
        isActive: false,
        disabled: false,
        external: false,
        ...link,
      })),
    [links]
  )

  return (
    <>
      {/* 移动端下拉菜单 */}
      <div className='lg:hidden'>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger
            render={<Button size='icon' variant='outline' className='size-7' />}
          >
            <Menu />
          </DropdownMenuTrigger>
          <DropdownMenuContent side='bottom' align='start'>
            {normalizedLinks.map(
              ({ title, href, isActive, disabled, external, appLink }) => (
                <DropdownMenuItem
                  key={`${title}-${href}`}
                  render={
                    external ? (
                      // APIDUO:dynamic-nav:BEGIN
                      // A client deep link is handed to the operating system,
                      // so a new tab would only be left behind empty.
                      <a
                        href={href}
                        target={appLink ? undefined : '_blank'}
                        rel={appLink ? undefined : 'noopener noreferrer'}
                        onClick={(event) => handleExternalClick(event, href)}
                        className={!isActive ? 'text-muted-foreground' : ''}
                      >
                        {title}
                      </a>
                      // APIDUO:dynamic-nav:END
                    ) : (
                      <Link
                        to={href}
                        className={!isActive ? 'text-muted-foreground' : ''}
                        disabled={disabled}
                      >
                        {title}
                      </Link>
                    )
                  }
                ></DropdownMenuItem>
              )
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* 桌面端水平导航 */}
      <nav
        className={cn(
          'hidden items-center space-x-4 lg:flex lg:space-x-4 xl:space-x-6',
          className
        )}
        {...props}
      >
        {normalizedLinks.map(({ title, href, isActive, disabled, external, appLink }) =>
          external ? (
            // APIDUO:dynamic-nav:BEGIN
            // Same rule as the mobile menu: a deep link keeps the current tab.
            <a
              key={`${title}-${href}`}
              href={href}
              target={appLink ? undefined : '_blank'}
              rel={appLink ? undefined : 'noopener noreferrer'}
              onClick={(event) => handleExternalClick(event, href)}
              className={`hover:text-primary text-sm font-medium transition-colors ${isActive ? '' : 'text-muted-foreground'}`}
            >
              {title}
            </a>
            // APIDUO:dynamic-nav:END
          ) : (
            <Link
              key={`${title}-${href}`}
              to={href}
              disabled={disabled}
              className={`hover:text-primary text-sm font-medium transition-colors ${isActive ? '' : 'text-muted-foreground'}`}
            >
              {title}
            </Link>
          )
        )}
      </nav>
    </>
  )
}
