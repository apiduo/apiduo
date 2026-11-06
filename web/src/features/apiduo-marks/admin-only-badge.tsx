import { useTranslation } from 'react-i18next'

/**
 * "Admin Only" marker for a dashboard section whose data is global instead of
 * scoped to the caller.
 *
 * It sits in the page title rather than in the tab strip on purpose. Six of the
 * seven tabs are admin-only, so a tab-level badge would mark almost everything
 * and distinguish nothing; in the title it answers the question the reader
 * actually has while looking at the numbers — can the users I am billing see
 * this page?
 *
 * The label reuses the `Admin Only` key that already exists in all seven
 * locales, so the mark costs no new translations.
 */
export function AdminOnlyBadge({ visible }: { visible?: boolean }) {
  const { t } = useTranslation()
  if (!visible) return null
  return <span data-apiduo='admin-badge'>{t('Admin Only')}</span>
}
