import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ConnectDialog } from "./cloud-connections"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: { orgId: "org" } }),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const key = JSON.stringify({ type: "service_account", project_id: "shop" })

function stub(onPost: (body: Record<string, unknown>) => void) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (path.includes("connection-clouds"))
        return Response.json({ clouds: [] })
      if (init?.method === "POST" && path.endsWith("/connections")) {
        onPost(JSON.parse(String(init.body)) as Record<string, unknown>)
        return Response.json({
          id: "gcp-shop",
          name: "Shop",
          cloud: "gcp",
          account: "shop",
          status: "ready",
          details: {},
          missing: [],
        })
      }
      return Response.json({ clouds: [], connections: [] })
    })
  )
}

it("sends the billing export table, which Google Cloud's spend is read from", async () => {
  let submitted: Record<string, unknown> | undefined
  stub((body) => (submitted = body))
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud="gcp" onOpenChange={vi.fn()} />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText(/^Name$/i), "Shop")
  await user.click(screen.getByLabelText(/Service account key/i))
  await user.paste(key)
  await user.type(
    screen.getByLabelText(/Billing export table/i),
    "shop-prod.billing.gcp_billing_export_v1_0A1B2C"
  )
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(submitted).toBeDefined())
  expect(submitted?.settings).toEqual({
    billing_export: "shop-prod.billing.gcp_billing_export_v1_0A1B2C",
  })
})

it("leaves the billing export out when it's empty", async () => {
  let submitted: Record<string, unknown> | undefined
  stub((body) => (submitted = body))
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud="gcp" onOpenChange={vi.fn()} />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText(/^Name$/i), "Shop")
  await user.click(screen.getByLabelText(/Service account key/i))
  await user.paste(key)
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(submitted).toBeDefined())
  expect(submitted?.settings).toBeUndefined()
})
