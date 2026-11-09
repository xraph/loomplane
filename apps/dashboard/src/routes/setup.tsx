import { createFileRoute } from "@tanstack/react-router"

// App renders this screen itself while the instance has no accounts, and
// sends everyone else to sign-in. The route only reserves the URL, so it
// renders nothing.
export const Route = createFileRoute("/setup")({
  component: () => null,
})
