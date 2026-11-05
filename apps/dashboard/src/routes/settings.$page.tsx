import { createFileRoute, notFound } from "@tanstack/react-router"
import { lazy, Suspense } from "react"

import { PageLoading } from "@/components/route-feedback"

// Settings pages only another edition adds. A public build has none, and
// Vite folds the comparison to false there, so nothing below it ships.
const EditionPage =
  import.meta.env.VITE_LOOMPLANE_EDITION === "cloud"
    ? lazy(() =>
        import("@/cloud/pages").then((m) => ({ default: m.SettingsPage }))
      )
    : null

export const Route = createFileRoute("/settings/$page")({
  beforeLoad: () => {
    if (import.meta.env.VITE_LOOMPLANE_EDITION !== "cloud") throw notFound()
  },
  component: SettingsEditionPage,
})

function SettingsEditionPage() {
  const { page } = Route.useParams()
  if (!EditionPage) return null
  return (
    <Suspense fallback={<PageLoading />}>
      <EditionPage page={page} />
    </Suspense>
  )
}
