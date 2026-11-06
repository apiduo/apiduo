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
  /**
   * Slug of the parent entry. It forms a two-level menu: the parent is a
   * heading and may not carry a target of its own.
   */
  parent_slug?: string
  /** Group this entry belongs to. Empty means the default group. */
  group_slug?: string
}

/**
 * An administrator-defined sidebar heading.
 *
 * Groups are stored apart from entries so a heading can exist before it has
 * content, and so its name and order survive independently.
 */
export type NavAppGroup = {
  slug: string
  name: string
  name_i18n: string
  sort: number
}

export type NavAppListResponse = {
  success: boolean
  message: string
  /** Present when the server returned a translatable message. */
  message_key?: string
  message_params?: Record<string, unknown>
  /** Content hash of the stored list; returned by the admin endpoints. */
  revision?: string
  /** Administrator-configured heading for the default group. */
  group_title?: string
  /** Administrator-defined groups. */
  groups?: NavAppGroup[] | null
  /** Top-level sidebar order, as group ids. */
  group_order?: string[] | null
  data: NavApp[] | null
}

export type SaveNavAppsRequest = {
  apps: NavApp[]
  /** Heading of the default group. Empty restores the translated default. */
  group_title?: string
  /** Replaces the group list. */
  groups?: NavAppGroup[]
  /** Top-level sidebar order, as group ids. */
  group_order?: string[]
  /**
   * Revision the editor loaded. The server rejects the save when it no longer
   * matches, instead of overwriting a concurrent change.
   */
  base_revision?: string
}
