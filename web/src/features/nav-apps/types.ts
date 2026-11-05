export type NavAppDisplayType = 'external' | 'iframe'

export type NavAppPlacement = 'side' | 'top' | 'both'

/**
 * An administrator-configured navigation entry.
 *
 * Stored as a JSON array in the existing `CustomNavApps` option, so no database
 * schema is involved. `slug` is the stable identity and is immutable.
 */
export type NavApp = {
  slug: string
  name: string
  /** Optional JSON object of language tag to display name. */
  name_i18n: string
  icon: string
  href: string
  display_type: NavAppDisplayType
  placement: NavAppPlacement
  min_role: number
  sort: number
  enabled: boolean
}

export type NavAppListResponse = {
  success: boolean
  message: string
  /** Present when the server returned a translatable message. */
  message_key?: string
  message_params?: Record<string, unknown>
  /** Content hash of the stored list; returned by the admin endpoints. */
  revision?: string
  data: NavApp[] | null
}

export type SaveNavAppsRequest = {
  apps: NavApp[]
  /**
   * Revision the editor loaded. The server rejects the save when it no longer
   * matches, instead of overwriting a concurrent change.
   */
  base_revision?: string
}
