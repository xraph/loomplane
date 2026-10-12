import { createFileRoute, Navigate } from "@tanstack/react-router"

export const Route = createFileRoute("/sign-in")({
  component: () => <Navigate to="/" replace />,
})
