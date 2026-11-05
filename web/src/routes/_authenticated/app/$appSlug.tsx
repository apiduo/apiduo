import { createFileRoute } from '@tanstack/react-router'

import { NavAppFrame } from '@/features/nav-apps/iframe-view'

// APIDUO:dynamic-nav:BEGIN
// Container route for embedded navigation entries. Only iframe entries link
// here; external entries open directly, so no route is created for them.
export const Route = createFileRoute('/_authenticated/app/$appSlug')({
  component: AppFrameRoute,
})

function AppFrameRoute() {
  const { appSlug } = Route.useParams()
  return <NavAppFrame slug={appSlug} />
}
// APIDUO:dynamic-nav:END
