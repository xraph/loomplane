import { createFileRoute } from "@tanstack/react-router"

import { Domains, type DomainsSearch } from "@/pages/domains"

// IDs are short; anything else in the URL is dropped.
function id(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 && value.length <= 64
    ? value
    : undefined
}

export const Route = createFileRoute("/domains")({
  component: DomainsRoute,
  validateSearch: (search: Record<string, unknown>): DomainsSearch => {
    const out: DomainsSearch = {}
    const registration = id(search.registration)
    const zone = id(search.zone)
    if (registration) out.registration = registration
    if (search.action === "auto-renew-off") out.action = "auto-renew-off"
    if (zone) out.zone = zone
    // All is the default, so only "attention" lands in the URL.
    if (search.show === "attention") out.show = "attention"
    return out
  },
})

function DomainsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <Domains
      search={search}
      onSearch={(next) => void navigate({ search: next, replace: true })}
    />
  )
}
