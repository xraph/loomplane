import { Navigate, createFileRoute } from "@tanstack/react-router"

import { Problem } from "@/App"
import { inviteSettled, usePendingInvite } from "@/lib/invite"
import { AcceptInvitation } from "@/pages/accept-invitation"

// The root captures an invitation link's token before authentication and
// takes it out of the URL, so the page reads it from the tab, not the
// address. App only renders this route once you're signed in.
export const Route = createFileRoute("/invitations/accept")({
  component: Invitation,
})

function Invitation() {
  const invite = usePendingInvite()
  if (invite) return <AcceptInvitation token={invite} />
  // Accepted or put off a moment ago: there's nothing left to show here.
  if (inviteSettled()) return <Navigate to="/" replace />
  return (
    <Problem
      title="This invitation link is incomplete"
      message="Open the full link from your invitation email, including its token."
      retry={false}
    />
  )
}
