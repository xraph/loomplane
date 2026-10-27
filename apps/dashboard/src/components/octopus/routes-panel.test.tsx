import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { RoutesPanel } from "@/components/octopus/routes-panel"
import {
  card,
  here,
  mockBoard,
  mount,
  panelProps,
  row,
  serve,
} from "@/components/octopus/test-api"
import {
  catalogKind,
  type CatalogPanel,
  type CatalogPanelProps,
} from "@/lib/catalog-kinds"

const permissions = vi.hoisted(() => ({
  can: (() => true) as (permission: string) => boolean,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (p: string) => permissions.can(p) }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  forgeClient.clear()
  permissions.can = () => true
})

const hostCard = (hostname: string) =>
  screen.findByRole("region", { name: hostname })

test("renders one card per domain with pills and route rows", async () => {
  serve()
  mount(<RoutesPanel {...panelProps()} />)
  const api = await hostCard("api.acme.dev")
  expect(await hostCard("acme.dev")).toBeVisible()
  expect(await hostCard("*.acme.dev")).toBeVisible()
  expect(
    screen
      .getAllByRole("region")
      .map((r) => r.getAttribute("aria-label"))
      .filter(
        (label) => label !== "Discovery" && label !== "Services without a route"
      )
  ).toEqual(["acme.dev", "api.acme.dev", "*.acme.dev"])

  expect(within(api).getByText("FARP routes land here")).toBeVisible()
  expect(within(api).getByText("4 routes")).toBeVisible()
  expect(within(api).getByText("DNS verified")).toBeVisible()
  expect(within(api).getByText("Certificate active")).toBeVisible()
  expect(
    within(await hostCard("*.acme.dev")).getByText("Certificate pending")
  ).toBeVisible()
  expect(within(await hostCard("acme.dev")).getByText("1 route")).toBeVisible()

  const auth = within(api).getByRole("row", { name: /\/auth\/\*/ })
  expect(within(auth).getByText("Added here")).toBeVisible()
  expect(within(auth).getByText("Overrides FARP")).toBeVisible()
  expect(within(auth).getByText("Strips /auth before forwarding")).toBeVisible()
  expect(within(auth).getByText("Any")).toBeVisible()
  expect(within(auth).getByRole("button", { name: "Edit" })).toBeVisible()

  const orders = within(api).getByRole("row", { name: /\/v1\/orders\/\*/ })
  expect(within(orders).getByText("FARP")).toBeVisible()
  expect(within(orders).getByText("GET POST")).toBeVisible()
  expect(within(orders).getByText(":8080")).toBeVisible()
  expect(within(orders).getByRole("button", { name: "Override" })).toBeVisible()
  expect(within(api).getAllByRole("button", { name: "Override" })).toHaveLength(
    3
  )
  expect(within(api).getByRole("button", { name: "Route" })).toBeVisible()

  const strip = screen.getByRole("region", { name: "Discovery" })
  await vi.waitFor(() =>
    expect(strip).toHaveTextContent(
      "FARP is watching acme-shop / production. 3 services publish schemas, and their routes land on api.acme.dev."
    )
  )
  expect(within(strip).getByText("acme-shop / production").tagName).toBe("B")
  expect(within(strip).getByText("Last change 2 minutes ago")).toBeVisible()
  expect(screen.getByRole("button", { name: "Add route" })).toBeVisible()
})

test("source filter narrows rows and shows the empty state", async () => {
  serve()
  mount(<RoutesPanel {...panelProps()} />)
  const filter = await screen.findByRole("radiogroup", { name: "Route source" })
  expect(
    within(filter)
      .getAllByRole("radio")
      .map((r) => r.textContent)
  ).toEqual(["All 6", "FARP 3", "Added here 3"])
  expect(within(filter).getByRole("radio", { name: "All 6" })).toBeChecked()

  await userEvent.click(within(filter).getByRole("radio", { name: "FARP 3" }))
  expect(within(filter).getByRole("radio", { name: "FARP 3" })).toBeChecked()
  expect(screen.queryByText("Added here", { selector: "span" })).toBeNull()
  expect(screen.queryByRole("region", { name: "acme.dev" })).toBeNull()
  expect(
    within(await hostCard("api.acme.dev")).getByText("3 routes")
  ).toBeVisible()

  await userEvent.click(
    within(filter).getByRole("radio", { name: "Added here 3" })
  )
  expect(screen.queryByText("FARP", { selector: "span" })).toBeNull()
  expect(await hostCard("acme.dev")).toBeVisible()
})

test("a filter with nothing to show says so", async () => {
  const board = mockBoard()
  board.hosts = [
    card("acme.dev", [
      here({ path: "/", note: "Storefront", service: "web", port: 3000 }),
    ]),
  ]
  board.counts = { all: 1, farp: 0, here: 1 }
  serve({ board })
  mount(<RoutesPanel {...panelProps()} />)
  await userEvent.click(await screen.findByRole("radio", { name: "FARP 0" }))
  expect(screen.getByText("No routes from this source")).toBeVisible()
  expect(
    screen.getByText("Pick All to see every route on this gateway.")
  ).toBeVisible()
  expect(screen.queryByRole("region", { name: "acme.dev" })).toBeNull()
})

test("lists services without a route and ones that can't take one", async () => {
  const board = mockBoard()
  board.unroutable = [
    { service: "worker", reason: "no-port" },
    { service: "batch", reason: "other-target" },
  ]
  serve({ board })
  mount(<RoutesPanel {...panelProps()} />)
  const unrouted = await screen.findByRole("region", {
    name: "Services without a route",
  })
  expect(unrouted).toHaveTextContent(
    "admin :4000 has no route yet. worker has no HTTP port, so it can't take one. batch runs on a different server or cluster, so Octopus can't reach it."
  )
  expect(within(unrouted).getByText("admin").tagName).toBe("B")
  expect(
    within(unrouted).getByRole("button", { name: "Route admin" })
  ).toBeVisible()
})

test("shows the live error and keeps manual routes when Octopus is down", async () => {
  const board = mockBoard()
  board.live = false
  board.live_error =
    "Octopus isn't answering on its private port, so FARP routes can't be shown. Your routes are kept and pushed again when it's back."
  board.hosts = [
    card("acme.dev", [
      here({
        path: "/",
        note: "Storefront",
        service: "web",
        port: 3000,
        sync: "pending",
      }),
      here({
        path: "/shop/*",
        service: "web",
        port: 3000,
        sync: "failed",
        sync_error:
          "Octopus refused the route: upstream lp-web-3000 is unknown.",
      }),
    ]),
  ]
  board.counts = { all: 2, farp: 0, here: 2 }
  serve({ board })
  mount(<RoutesPanel {...panelProps()} />)
  const strip = await screen.findByRole("region", { name: "Discovery" })
  expect(within(strip).getByText(String(board.live_error))).toBeVisible()
  const acme = await hostCard("acme.dev")
  expect(within(acme).getByText("Pushing to Octopus")).toBeVisible()
  expect(
    within(acme).getByText(
      "Octopus refused the route: upstream lp-web-3000 is unknown."
    )
  ).toBeVisible()
  expect(within(acme).getByText("2 routes")).toBeVisible()
})

test("any hostname, detached hosts and admin routes", async () => {
  const board = mockBoard()
  ;(board.gateway as { farp_hostnames: string[] }).farp_hostnames = []
  board.hosts = [
    card("", [row({ path: "/v1/orders/*", note: "From the OpenAPI schema" })], {
      farp_home: true,
    }),
    card(
      "old.acme.dev",
      [here({ path: "/legacy/*", service: "web", port: 3000 })],
      { attached: false, status: undefined }
    ),
    card("acme.dev", [
      row({
        source: "octopus-admin",
        path: "/debug",
        note: "Added in the Octopus admin. It won't survive a restart, so add it here instead.",
        service: "",
        port: 0,
      }),
    ]),
  ]
  serve({ board })
  mount(<RoutesPanel {...panelProps()} />)
  const any = await hostCard("Any hostname")
  expect(any).toHaveTextContent(
    "Reachable on the target's private network at http://edge--gateway:8080"
  )
  expect(within(any).queryByText(/^DNS/)).toBeNull()
  expect(
    within(await hostCard("old.acme.dev")).getByText("Not attached")
  ).toBeVisible()
  const admin = within(await hostCard("acme.dev")).getByRole("row", {
    name: /\/debug/,
  })
  expect(within(admin).getByText("Octopus admin")).toBeVisible()
  expect(within(admin).queryByRole("button")).toBeNull()
  expect(screen.getByRole("region", { name: "Discovery" })).toHaveTextContent(
    "their routes answer on every hostname until you pick where they land."
  )
})

test("hides write actions without infrastructure:modify", async () => {
  permissions.can = (p) => p !== "infrastructure:modify"
  serve()
  mount(<RoutesPanel {...panelProps()} />)
  const api = await hostCard("api.acme.dev")
  expect(within(api).getByText("4 routes")).toBeVisible()
  for (const name of ["Add route", "Route", "Override", "Edit", "Route admin"])
    expect(screen.queryByRole("button", { name })).toBeNull()
})

test("loading, denied access and a failed read stay distinct", async () => {
  serve({ boardPending: true })
  const view = mount(<RoutesPanel {...panelProps()} />)
  expect(await screen.findByLabelText("Loading routes")).toBeVisible()
  view.unmount()
  forgeClient.clear()

  serve({ boardStatus: 403 })
  const denied = mount(<RoutesPanel {...panelProps()} />)
  expect(
    await screen.findByText("You can't see this gateway's routes")
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  denied.unmount()
  forgeClient.clear()

  serve({ boardStatus: 500 })
  mount(<RoutesPanel {...panelProps()} />)
  expect(await screen.findByRole("button", { name: "Retry" })).toBeVisible()
  expect(screen.queryByText("You can't see this gateway's routes")).toBeNull()
})

function TabLabels(props: CatalogPanelProps) {
  const octopus = catalogKind("octopus")!
  return (
    <ul>
      {octopus.panels.map((panel) => (
        <TabLabel key={panel.id} panel={panel} props={props} />
      ))}
    </ul>
  )
}

function TabLabel({
  panel,
  props,
}: {
  panel: CatalogPanel
  props: CatalogPanelProps
}) {
  const useCount = panel.useCount ?? noCount
  const count = useCount(props)
  return (
    <li>{count === undefined ? panel.title : `${panel.title} ${count}`}</li>
  )
}

function noCount(): number | undefined {
  return undefined
}

test("tabs show counts from useCount", async () => {
  const octopus = catalogKind("octopus")!
  expect(octopus.panels.map((p) => [p.id, p.title, p.order])).toEqual([
    ["routes", "Routes", 1],
    ["domains", "Domains", 2],
    ["discovery", "Discovery", 3],
    ["settings", "Settings", 4],
  ])
  expect(octopus.group).toBe("gateway")
  expect(octopus.blurb).toBe(
    "API gateway that routes your services by domain and path."
  )
  serve()
  mount(<TabLabels {...panelProps()} />)
  expect(await screen.findByText("Routes 6")).toBeVisible()
  expect(screen.getByText("Domains 3")).toBeVisible()
  expect(screen.getByText("Discovery")).toBeVisible()
  expect(screen.getByText("Settings")).toBeVisible()
})
