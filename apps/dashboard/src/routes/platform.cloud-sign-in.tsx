import { createFileRoute } from "@tanstack/react-router"

import { CloudSignIn } from "@/pages/cloud-sign-in"

export const Route = createFileRoute("/platform/cloud-sign-in")({
  component: CloudSignIn,
})
