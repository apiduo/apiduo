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
  // These read the global aggregates (/api/data, /api/data/flow, /api/data/sk).
  // Regular users only have the /self variants, so showing the tabs would just
  // render a 403. Their own view lives in "My keys".
  'models',
  'flow',
  'users',
  'sk',
  'capacity',
  'retention',
]
