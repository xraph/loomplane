import { createFileRoute, redirect } from "@tanstack/react-router"

type Search = { connection?: string }

// Resources moved under Integrations. Links and bookmarks to the old page
// land on the new tab, keeping the account they were filtered to.
export const Route = createFileRoute("/resources/")({
  validateSearch: (search: Record<string, unknown>): Search =>
    typeof search.connection === "string" && search.connection.trim()
      ? { connection: search.connection.trim() }
      : {},
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/integrations/resources", search, replace: true })
  },
})
