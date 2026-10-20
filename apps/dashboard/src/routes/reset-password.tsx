import { createFileRoute } from "@tanstack/react-router"
import { RecoveryPage } from "@/pages/recovery"

export const Route = createFileRoute("/reset-password")({
  validateSearch: (search: Record<string, unknown>) => ({
    token:
      typeof search.token === "string" && search.token.length <= 512
        ? search.token
        : "",
  }),
  component: ResetPassword,
})

function ResetPassword() {
  const { token } = Route.useSearch()
  return <RecoveryPage mode="reset-password" token={token} />
}
