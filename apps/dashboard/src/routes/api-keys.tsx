import { createFileRoute, redirect } from "@tanstack/react-router"

// API keys moved under /settings. Links and bookmarks to the old address
// still land there.
export const Route = createFileRoute("/api-keys")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/api-keys", replace: true })
  },
})
