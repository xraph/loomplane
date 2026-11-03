import { createFileRoute, redirect } from "@tanstack/react-router"

// Your account opens on its profile.
export const Route = createFileRoute("/account/")({
  beforeLoad: () => {
    throw redirect({ to: "/account/profile", replace: true })
  },
})
