import { createFileRoute } from "@tanstack/react-router"

import { GitHubSetup } from "@/pages/github-setup"

// GitHub's install redirect: the GitHub App's callback URL points here.
// The router parses search values as JSON, so the installation ID may
// arrive as a number.
const text = (v: unknown) =>
  typeof v === "string" || typeof v === "number" ? String(v) : ""

export const Route = createFileRoute("/integrations/github/setup")({
  validateSearch: (search: Record<string, unknown>) => ({
    installation_id: text(search.installation_id),
    code: text(search.code),
    state: text(search.state),
  }),
  component: SetupRoute,
})

function SetupRoute() {
  const s = Route.useSearch()
  return (
    <GitHubSetup
      installationId={s.installation_id}
      code={s.code}
      state={s.state}
    />
  )
}
