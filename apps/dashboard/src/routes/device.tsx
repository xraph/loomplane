import { createFileRoute } from "@tanstack/react-router"

import { Device } from "@/pages/device"

// loomctl login opens this page with its code. App renders it once you're
// signed in, outside any workspace: the sign-in it approves isn't tied to
// one. Signing in first keeps the code, since the sign-in redirect keeps
// the whole address.
export const Route = createFileRoute("/device")({
  validateSearch: (search: Record<string, unknown>) => ({
    code: typeof search.code === "string" ? search.code.slice(0, 16) : "",
  }),
  component: DevicePage,
})

function DevicePage() {
  const { code } = Route.useSearch()
  return <Device code={code} />
}
