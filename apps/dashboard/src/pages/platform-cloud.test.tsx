import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setInstance } from "@/lib/instance"
import { PlatformCloud } from "@/pages/platform-cloud"

vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/route-feedback", () => ({
  PageNotFound: () => <h1>Page not found</h1>,
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
  })
})

it("shows the page to a platform admin", () => {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin: true,
  })
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ hosts: [], regions: [], tenants: [] }))
  )
  render(
    <ClientProvider client={forgeClient}>
      <PlatformCloud />
    </ClientProvider>
  )
  expect(screen.getByRole("heading", { name: "Platform Cloud" })).toBeTruthy()
  const names = screen
    .getAllByRole("heading", { level: 2 })
    .map((h) => h.textContent)
  expect(names).toEqual(["Hosts", "Regions", "Workspaces", "Settings"])
})

it("shows anyone else the not-found view", () => {
  render(<PlatformCloud />)
  expect(screen.getByText("Page not found")).toBeTruthy()
  expect(screen.queryByRole("heading", { name: "Platform Cloud" })).toBeNull()
})
