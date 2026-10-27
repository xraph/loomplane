import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DiscoveryPanel } from "@/components/octopus/discovery-panel"
import {
  json,
  mockBoard,
  mockDiscovery,
  mockStatus,
  mount,
  panelProps,
  serve,
} from "@/components/octopus/test-api"

const permissions = vi.hoisted(() => ({
  can: (() => true) as (permission: string) => boolean,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (p: string) => permissions.can(p) }),
}))
const toasts = vi.hoisted(() => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }),
}))
vi.mock("sonner", () => ({ toast: toasts.toast }))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  toasts.toast.mockReset()
  toasts.toast.error.mockReset()
  forgeClient.clear()
  permissions.can = () => true
})

const optIn =
  "Add a farp block to a service in the manifest. Octopus picks up its schemas on the next release."

test("discovery tab explains Kubernetes and Docker modes", async () => {
  serve({
    discovery: mockDiscovery({
      mode: "kubernetes",
      namespace: "acme-prod",
      services: [
        {
          service: "api",
          opted_in: true,
          metadata: { "farp.openapi.path": "/openapi.json" },
          state: "discovered",
          routes: 3,
          updated_at: new Date().toISOString(),
        },
      ],
    }),
    board: {
      ...mockBoard(),
      gateway: { ...mockStatus(), provider: "kubernetes", farp_hostnames: [] },
    },
  })
  const view = mount(<DiscoveryPanel {...panelProps()} />)
  const mode = await screen.findByRole("region", {
    name: "How discovery works",
  })
  expect(mode).toHaveTextContent(
    "Octopus watches Services labelled farp.enabled=true in namespace acme-prod, with a Role that can only get, list and watch services, endpoints and endpointslices there."
  )
  expect(within(mode).getByText("acme-prod")).toHaveClass("font-mono")
  expect(screen.queryByRole("button", { name: "Register again" })).toBeNull()
  const landing = screen.getByRole("region", { name: "Where FARP routes land" })
  expect(await within(landing).findByText("Every hostname")).toBeVisible()
  expect(within(landing).getByText("Change on the Domains tab")).toBeVisible()

  const optInBlock = screen.getByRole("region", { name: "How to opt in" })
  expect(within(optInBlock).getByText(optIn)).toBeVisible()
  expect(optInBlock.querySelector("pre")).toHaveTextContent(
    "services: billing: port: 8080 farp: openapi: /openapi.json capabilities: [rest, websocket]"
  )
  view.unmount()
  forgeClient.clear()

  serve()
  mount(<DiscoveryPanel {...panelProps()} />)
  expect(
    await screen.findByText(
      "A Docker server has nothing to watch, so Loomplane registers each opted-in service with Octopus on every release and sends a heartbeat every 30 seconds."
    )
  ).toBeVisible()
  const lands = screen.getByRole("region", { name: "Where FARP routes land" })
  expect(await within(lands).findByText("api.acme.dev")).toHaveClass(
    "font-mono"
  )
  expect(within(lands).queryByText("Every hostname")).toBeNull()
})

test("where FARP routes land leaves out detached hostnames", async () => {
  const b = mockBoard() as Record<string, unknown> & {
    gateway: { farp_hostnames: string[] }
  }
  b.gateway.farp_hostnames = ["api.acme.dev", "gone.acme.dev"]
  serve({ board: b })
  const view = mount(<DiscoveryPanel {...panelProps()} />)
  const landing = await screen.findByRole("region", {
    name: "Where FARP routes land",
  })
  expect(await within(landing).findByText("api.acme.dev")).toBeVisible()
  expect(within(landing).queryByText("gone.acme.dev")).toBeNull()
  view.unmount()
  forgeClient.clear()

  // Only detached ones picked: FARP routes answer on every hostname.
  b.gateway.farp_hostnames = ["gone.acme.dev"]
  serve({ board: b })
  mount(<DiscoveryPanel {...panelProps()} />)
  const every = await screen.findByRole("region", {
    name: "Where FARP routes land",
  })
  expect(await within(every).findByText("Every hostname")).toBeVisible()
  expect(within(every).queryByText("gone.acme.dev")).toBeNull()
})

test("a failed push says why", async () => {
  serve({
    write: (w) =>
      w.path.endsWith("/sync")
        ? json({
            state: "failed",
            generation: 5,
            restarted: false,
            restored: 0,
            message: "Octopus refused billing's registration.",
          })
        : undefined,
  })
  mount(<DiscoveryPanel {...panelProps()} />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Register again" })
  )
  await vi.waitFor(() =>
    expect(toasts.toast.error).toHaveBeenCalledWith(
      "Octopus refused billing's registration."
    )
  )
})

test("discovery lists services with their state and lets Docker register again", async () => {
  const writes = serve()
  mount(<DiscoveryPanel {...panelProps()} />)
  const table = await screen.findByRole("table", { name: "FARP services" })
  const rows = within(table).getAllByRole("row").slice(1)
  expect(
    rows.map((r) => r.querySelector("[data-service]")?.textContent)
  ).toEqual(["api", "billing", "auth", "search"])

  const billing = rows[1]
  expect(within(billing).getByText("/openapi.json")).toHaveClass("font-mono")
  expect(within(billing).getByText("/asyncapi.json")).toBeVisible()
  expect(within(billing).getByText("Registered")).toHaveClass("bg-overdye-tint")
  expect(within(billing).getByText("2")).toBeVisible()
  expect(within(billing).getByText("2 minutes ago")).toBeVisible()

  const auth = rows[2]
  expect(within(auth).getByText("Failed")).toHaveClass("bg-failed-tint")
  expect(
    within(auth).getByText(
      "Octopus refused the registration: schema fetch timed out."
    )
  ).toBeVisible()

  const search = rows[3]
  expect(within(search).getByText("Waiting for a release")).toHaveClass(
    "bg-weld-tint"
  )
  expect(within(search).getByText("/graphql")).toBeVisible()

  await userEvent.click(screen.getByRole("button", { name: "Register again" }))
  await vi.waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/p/environments/env_prod/octopus/edge/sync",
        body: undefined,
      },
    ])
  )
})

test("register again is hidden without infrastructure:modify", async () => {
  permissions.can = (p) => p !== "infrastructure:modify"
  serve()
  mount(<DiscoveryPanel {...panelProps()} />)
  expect(
    await screen.findByRole("table", { name: "FARP services" })
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Register again" })).toBeNull()
})

test("a gateway with no FARP services says how to opt in", async () => {
  serve({ discovery: mockDiscovery({ services: [] }) })
  mount(<DiscoveryPanel {...panelProps()} />)
  expect(
    await screen.findByText("No service publishes FARP schemas yet.")
  ).toBeVisible()
  expect(screen.queryByRole("table", { name: "FARP services" })).toBeNull()
  expect(screen.getAllByText(optIn).length).toBeGreaterThan(0)
})

test("loading, denied and failed requests stay distinct on each tab", async () => {
  serve({ discoveryPending: true })
  const view = mount(<DiscoveryPanel {...panelProps()} />)
  expect(await screen.findByLabelText("Loading discovery")).toBeVisible()
  view.unmount()
  forgeClient.clear()

  serve({ discoveryStatus: 403 })
  const denied = mount(<DiscoveryPanel {...panelProps()} />)
  expect(
    await screen.findByText("You can't see this gateway's discovery")
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  denied.unmount()
  forgeClient.clear()

  serve({ discoveryStatus: 500 })
  const failed = mount(<DiscoveryPanel {...panelProps()} />)
  expect(await screen.findByRole("button", { name: "Retry" })).toBeVisible()
  expect(
    screen.queryByText("You can't see this gateway's discovery")
  ).toBeNull()
  failed.unmount()
  forgeClient.clear()

  // The landing hostnames come from the board; its failure stays in its section.
  serve({ boardStatus: 500 })
  mount(<DiscoveryPanel {...panelProps()} />)
  expect(
    await screen.findByRole("table", { name: "FARP services" })
  ).toBeVisible()
  const landing = screen.getByRole("region", { name: "Where FARP routes land" })
  expect(
    await within(landing).findByRole("button", { name: "Retry" })
  ).toBeVisible()
})
