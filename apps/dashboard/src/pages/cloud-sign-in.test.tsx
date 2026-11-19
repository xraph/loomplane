import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setInstance } from "@/lib/instance"
import { CloudSignIn } from "@/pages/cloud-sign-in"

vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/route-feedback", () => ({
  PageNotFound: () => <h1>Page not found</h1>,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => true,
    session: { orgId: "org", userId: "u1" },
  }),
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

function renderPage() {
  vi.stubGlobal("fetch", async () =>
    Response.json({
      apps: [],
      callback_url: "https://loomplane.example/cb",
      keyless: { enabled: true, issuer: "x", reason: "" },
      members: [],
    })
  )
  return render(
    <ClientProvider client={forgeClient}>
      <CloudSignIn />
    </ClientProvider>
  )
}

it("shows the page to a platform admin", async () => {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin: true,
  })
  renderPage()
  expect(screen.getByRole("heading", { name: "Cloud sign-in" })).toBeTruthy()
  expect(
    screen.getByText(/Every organization on this instance uses the same app\./)
  ).toBeTruthy()
  expect(await screen.findByText("https://loomplane.example/cb")).toBeTruthy()
})

it("points a platform admin at the console's Integrations", async () => {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin: true,
    consolePath: "/__console/",
  })
  renderPage()
  expect(
    screen.getByRole("link", { name: "Open Integrations in the console" })
  ).toHaveAttribute("href", "/__console/@integrations")
  expect(await screen.findByText("https://loomplane.example/cb")).toBeTruthy()
})

it("shows no pointer without a console", async () => {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin: true,
  })
  renderPage()
  expect(screen.queryByText("Also in the instance console")).toBeNull()
  expect(await screen.findByText("https://loomplane.example/cb")).toBeTruthy()
})

it("shows anyone else the not-found view", () => {
  renderPage()
  expect(screen.getByText("Page not found")).toBeTruthy()
  expect(screen.queryByRole("heading", { name: "Cloud sign-in" })).toBeNull()
})
