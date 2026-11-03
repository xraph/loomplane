import { Navigate, createFileRoute } from "@tanstack/react-router"

// App renders onboarding itself while you have no workspace, at this
// address. Once you have one, the workspace gate lets this route render, and
// there's nothing left to set up: go home.
export const Route = createFileRoute("/onboarding")({
  component: () => <Navigate to="/" replace />,
})
