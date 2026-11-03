import { createFileRoute } from "@tanstack/react-router"

import { NewWorkspace } from "@/pages/onboarding"

// App renders this outside the sidebar and top bar, in the onboarding frame,
// so a second workspace starts the way the first one did.
export const Route = createFileRoute("/workspaces/new")({
  component: NewWorkspace,
})
