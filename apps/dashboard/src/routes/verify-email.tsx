import { createFileRoute } from "@tanstack/react-router"
import { RecoveryPage } from "@/pages/recovery"

export const Route = createFileRoute("/verify-email")({
  validateSearch: (search: Record<string, unknown>) => ({
    email:
      typeof search.email === "string" && search.email.length <= 254
        ? search.email
        : "",
  }),
  component: VerifyEmail,
})

function VerifyEmail() {
  const { email } = Route.useSearch()
  return <RecoveryPage mode="verify-email" email={email} />
}
