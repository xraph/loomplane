import { createFileRoute } from "@tanstack/react-router"

// App renders the step itself, in the auth frame, to a platform admin whose
// instance still has the offer. The route only reserves the URL.
export const Route = createFileRoute("/setup_/cloud-sign-in")({
  component: () => null,
})
