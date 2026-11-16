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
import type { DashboardSectionId } from './section-registry'

// Single source of truth for sections that need an admin role. The nav and the
// section tab list both filter through this list, so adding an admin-only
// section here is enough to keep them in sync.
export const ADMIN_ONLY_DASHBOARD_SECTIONS: readonly DashboardSectionId[] = [
  // These read the global aggregates (/api/data, /api/data/flow).
  // Regular users only have the /self variants, so showing the tabs would just
  // render a 403.
  //
  // 'sk' 不在这个列表里：它的组件按 isAdmin 自动在 /api/data/sk 与
  // /api/data/sk/self 之间切换，普通用户看到的是自己 key 的用量。
  // 下放它是有意的——「我的 key 用得怎么样」本来就该让用户自己看。
  'models',
  'flow',
  'users',
]
