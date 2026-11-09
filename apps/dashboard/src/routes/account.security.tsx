import { createFileRoute } from "@tanstack/react-router"

import { AccountSecurity } from "@/pages/account-security"

export const Route = createFileRoute("/account/security")({
  component: AccountSecurity,
})
