import { ROLE } from '@/lib/roles'

export const navAppDisplayExternal = 'external'
export const navAppDisplayIframe = 'iframe'

export const navAppPlacementSide = 'side'
export const navAppPlacementTop = 'top'
export const navAppPlacementBoth = 'both'

/** Mirrors the backend cap so the form can warn before the save round-trip. */
export const NAV_APP_MAX_COUNT = 50

export const NAV_APP_ROLE_OPTIONS = [
  { value: ROLE.USER, labelKey: 'Signed-in users' },
  { value: ROLE.ADMIN, labelKey: 'Admins and above' },
  { value: ROLE.SUPER_ADMIN, labelKey: 'Super admin only' },
] as const
