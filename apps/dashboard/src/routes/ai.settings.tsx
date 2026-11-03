import { createFileRoute, redirect } from "@tanstack/react-router"

// AI settings moved under /settings. Links and bookmarks to the old address
// still land there.
export const Route = createFileRoute("/ai/settings")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/ai", replace: true })
  },
})
