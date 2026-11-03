import { createFileRoute } from "@tanstack/react-router"

import { AccountSessions } from "@/pages/account-sessions"

export const Route = createFileRoute("/account/sessions")({
  component: AccountSessions,
})
