import { createFileRoute } from "@tanstack/react-router"

import { CloudSignIn } from "@/pages/cloud-sign-in"

export const Route = createFileRoute("/cloud-sign-in")({
  component: CloudSignIn,
})
