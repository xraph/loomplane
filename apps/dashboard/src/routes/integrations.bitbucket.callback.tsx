import { createFileRoute } from "@tanstack/react-router"

import { BitbucketCallback } from "@/pages/bitbucket-callback"

// Bitbucket's OAuth redirect: the application's callback URL points here. The
// router parses search values as JSON, so a value may arrive as a number.
const text = (v: unknown) =>
  typeof v === "string" || typeof v === "number" ? String(v) : ""

export const Route = createFileRoute("/integrations/bitbucket/callback")({
  validateSearch: (search: Record<string, unknown>) => ({
    code: text(search.code),
    state: text(search.state),
    error: text(search.error),
  }),
  component: CallbackRoute,
})

function CallbackRoute() {
  const s = Route.useSearch()
  return <BitbucketCallback code={s.code} state={s.state} error={s.error} />
}
