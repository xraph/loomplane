import { createFileRoute } from "@tanstack/react-router"

import { Problem } from "@/App"

// The root captures valid invitation tokens before authentication. Reaching
// this route without one means the link was incomplete.
export const Route = createFileRoute("/invitations/accept")({
  component: () => (
    <Problem
      title="This invitation link is incomplete"
      message="Open the full link from your invitation email, including its token."
      retry={false}
    />
  ),
})
