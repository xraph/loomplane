import { createFileRoute } from "@tanstack/react-router"
import { RecoveryPage } from "@/pages/recovery"

export const Route = createFileRoute("/forgot-password")({
  component: () => <RecoveryPage mode="forgot-password" />,
})
