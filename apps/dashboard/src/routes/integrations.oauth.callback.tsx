import { createFileRoute } from "@tanstack/react-router"

import { OAuthCallback } from "@/pages/oauth-callback"

// Where a cloud sends someone back after they sign in. The API's callback
// URL for sign-in points here, so the path must match it exactly. The router
// parses search values as JSON, so a value may arrive as a number.
const text = (v: unknown) =>
  typeof v === "string" || typeof v === "number" ? String(v) : ""

export const Route = createFileRoute("/integrations/oauth/callback")({
  validateSearch: (search: Record<string, unknown>) => ({
    state: text(search.state),
    code: text(search.code),
    error: text(search.error),
    error_description: text(search.error_description),
    configurationId: text(search.configurationId),
  }),
  component: CallbackRoute,
})

function CallbackRoute() {
  const s = Route.useSearch()
  return (
    <OAuthCallback
      state={s.state}
      code={s.code}
      error={s.error}
      errorDescription={s.error_description}
      configurationId={s.configurationId}
    />
  )
}
