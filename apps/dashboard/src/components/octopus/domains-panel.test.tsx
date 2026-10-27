import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DomainsPanel } from "@/components/octopus/domains-panel"
import {
  card,
  domainRoutes,
  here,
  json,
  mockBoard,
  mount,
  operation,
  panelProps,
  row,
  serve,
  type Write,
} from "@/components/octopus/test-api"

const permissions = vi.hoisted(() => ({
  can: (() => true) as (permission: string) => boolean,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (p: string) => permissions.can(p) }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    search,
    className,
  }: {
    children: React.ReactNode
    to: string
    search?: Record<string, string>
    className?: string
  }) => (
    <a
      href={search ? `${to}?${new URLSearchParams(search)}` : to}
      className={className}
    >
      {children}
    </a>
  ),
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

type Json = Record<string, unknown>
type TestBoard = Json & {
  gateway: Json & { farp_hostnames: string[]; attach: Json }
  hosts: (Json & { routes: Json[] })[]
}

const board = () => mockBoard() as TestBoard

const posts = (writes: Write[]) => writes.filter((w) => w.method === "POST")

const hostRow = (hostname: string) =>
  screen.findByRole("row", {
    name: new RegExp(`^${hostname.replace(/[.*]/g, "\\$&")}`),
  })

test("domains tab lists attached hostnames with states and the FARP toggle", async () => {
  const b = board()
  b.gateway.farp_hostnames = ["api.acme.dev", "acme.dev"]
  // A hostname with routes but no domain route isn't attached, so it isn't listed.
  b.hosts.push(
    card("old.acme.dev", [here({ path: "/legacy/*" })], {
      attached: false,
      status: undefined,
    })
  )
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)

  const strip = await screen.findByRole("region", { name: "Domains" })
  expect(strip).toHaveTextContent(
    "Domains attached to edge. Each one is a Loomplane route to Octopus on port 8080, and Octopus routes by host and path behind it."
  )
  expect(
    within(strip).getByRole("button", { name: "Attach a domain" })
  ).toBeVisible()

  const table = screen.getByRole("table", { name: "Domains on edge" })
  const names = within(table)
    .getAllByRole("row")
    .slice(1)
    .map((r) => r.querySelector("[data-hostname]")?.textContent)
  expect(names).toEqual(["acme.dev", "api.acme.dev", "*.acme.dev"])
  expect(screen.queryByText("old.acme.dev")).toBeNull()

  const api = await hostRow("api.acme.dev")
  expect(within(api).getByText("api.acme.dev")).toHaveClass("font-mono")
  expect(within(api).getAllByText("Ready")).toHaveLength(2)
  expect(within(api).getByText("Active")).toBeVisible()
  expect(within(api).getByText("4 routes")).toBeVisible()
  expect(
    within(api).getByRole("switch", { name: "FARP routes land here" })
  ).toBeChecked()
  expect(
    within(api).getByRole("link", { name: "Zone settings" })
  ).toHaveAttribute("href", "/domains?zone=zone_dev")

  const wild = await hostRow("*.acme.dev")
  expect(within(wild).getByText("Issuing (DNS-01)")).toBeVisible()
  expect(within(wild).getByText("Waiting for certificate")).toBeVisible()
  expect(within(wild).getByText("1 route")).toBeVisible()
  expect(
    within(wild).getByRole("switch", { name: "FARP routes land here" })
  ).not.toBeChecked()

  const acme = await hostRow("acme.dev")
  await userEvent.click(
    within(acme).getByRole("switch", { name: "FARP routes land here" })
  )
  await vi.waitFor(() =>
    expect(posts(writes)).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/p/environments/env_prod/octopus/edge/farp-hostnames",
        body: { hostnames: ["api.acme.dev"], revision: 2 },
      },
    ])
  )
})

test("turning FARP on for a hostname adds it to the attached ones", async () => {
  const b = board()
  // A hostname that's no longer attached drops out of the list.
  b.gateway.farp_hostnames = ["api.acme.dev", "gone.acme.dev"]
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  const wild = await hostRow("*.acme.dev")
  await userEvent.click(
    within(wild).getByRole("switch", { name: "FARP routes land here" })
  )
  await vi.waitFor(() =>
    expect(posts(writes).map((w) => w.body)).toEqual([
      { hostnames: ["api.acme.dev", "*.acme.dev"], revision: 2 },
    ])
  )
})

test("with no hostname picked every FARP switch is on and turning one off narrows", async () => {
  const b = board()
  b.gateway.farp_hostnames = []
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  for (const name of ["acme.dev", "api.acme.dev", "*.acme.dev"])
    expect(
      within(await hostRow(name)).getByRole("switch", {
        name: "FARP routes land here",
      })
    ).toBeChecked()

  await userEvent.click(
    within(await hostRow("api.acme.dev")).getByRole("switch", {
      name: "FARP routes land here",
    })
  )
  await vi.waitFor(() =>
    expect(posts(writes).map((w) => w.body)).toEqual([
      { hostnames: ["acme.dev", "*.acme.dev"], revision: 2 },
    ])
  )
})

test("the last hostname FARP routes land on can't be turned off", async () => {
  const b = board()
  // gone.acme.dev isn't attached any more, so only api.acme.dev takes FARP.
  b.gateway.farp_hostnames = ["api.acme.dev", "gone.acme.dev"]
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  const last = within(await hostRow("api.acme.dev")).getByRole("switch", {
    name: "FARP routes land here",
  })
  expect(last).toBeChecked()
  expect(last).toHaveAttribute("aria-disabled", "true")
  await userEvent.click(last)
  await userEvent.hover(last.parentElement!)
  expect(
    await screen.findByText(
      "FARP routes have to land somewhere. Turn on another hostname first."
    )
  ).toBeInTheDocument()
  expect(posts(writes)).toEqual([])
  expect(
    within(await hostRow("acme.dev")).getByRole("switch", {
      name: "FARP routes land here",
    })
  ).not.toBeChecked()
})

test("with only detached hostnames picked no FARP switch is on and turning one on picks it alone", async () => {
  const b = board()
  b.gateway.farp_hostnames = ["gone.acme.dev"]
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  for (const name of ["acme.dev", "api.acme.dev", "*.acme.dev"]) {
    const toggle = within(await hostRow(name)).getByRole("switch", {
      name: "FARP routes land here",
    })
    expect(toggle).not.toBeChecked()
    expect(toggle).not.toHaveAttribute("aria-disabled", "true")
  }
  expect(screen.getByRole("region", { name: "Domains" })).toHaveTextContent(
    "FARP routes land on gone.acme.dev, which isn't attached any more. Pick a hostname on the Domains tab."
  )

  await userEvent.click(
    within(await hostRow("api.acme.dev")).getByRole("switch", {
      name: "FARP routes land here",
    })
  )
  await vi.waitFor(() =>
    expect(posts(writes).map((w) => w.body)).toEqual([
      { hostnames: ["api.acme.dev"], revision: 2 },
    ])
  )
})

test("detaching the last hostname FARP routes land on warns they go private", async () => {
  const b = board()
  b.gateway.farp_hostnames = ["api.acme.dev", "gone.acme.dev"]
  b.hosts = [
    card("acme.dev", []),
    card("api.acme.dev", [row({ path: "/v1/orders/*" })]),
  ]
  serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  const warning =
    "FARP routes land only here. After you detach it they answer only on the target's private network until you pick another hostname."

  await userEvent.click(
    within(await hostRow("api.acme.dev")).getByRole("button", {
      name: "Detach",
    })
  )
  let confirm = await screen.findByRole("alertdialog")
  expect(confirm).toHaveTextContent(warning)
  await userEvent.click(within(confirm).getByRole("button", { name: "Cancel" }))
  await vi.waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())

  // A hostname FARP routes don't land on detaches without the warning.
  await userEvent.click(
    within(await hostRow("acme.dev")).getByRole("button", { name: "Detach" })
  )
  confirm = await screen.findByRole("alertdialog")
  expect(confirm).not.toHaveTextContent(warning)
})

test("detaching the only hostname doesn't warn about FARP", async () => {
  const b = board()
  b.hosts = [card("api.acme.dev", [row({ path: "/v1/orders/*" })])]
  serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  await userEvent.click(
    within(await hostRow("api.acme.dev")).getByRole("button", {
      name: "Detach",
    })
  )
  expect(await screen.findByRole("alertdialog")).not.toHaveTextContent(
    "FARP routes land only here."
  )
})

test("a refused detach keeps the dialog open with why", async () => {
  const b = board()
  b.hosts = [card("api.acme.dev", [row({ path: "/v1/orders/*" })])]
  serve({
    board: b,
    domainRoutes: domainRoutes(),
    write: (w) =>
      w.path.endsWith("/remove")
        ? json(
            { message: "The DNS provider refused to delete the record." },
            422
          )
        : undefined,
  })
  mount(<DomainsPanel {...panelProps()} />)
  await userEvent.click(
    within(await hostRow("api.acme.dev")).getByRole("button", {
      name: "Detach",
    })
  )
  const confirm = await screen.findByRole("alertdialog")
  const action = within(confirm).getByRole("button", { name: "Detach" })
  // Detach waits for the domain route it removes.
  await vi.waitFor(() => expect(action).toBeEnabled())
  await userEvent.click(action)
  expect(await within(confirm).findByRole("alert")).toHaveTextContent(
    "The DNS provider refused to delete the record."
  )
  expect(screen.getByRole("alertdialog")).toBeVisible()
})

test("detach is blocked while routes use the hostname", async () => {
  const b = board()
  b.hosts = [
    card("acme.dev", [
      here({ path: "/", service: "web", port: 3000 }),
      here({ path: "/shop/*", service: "web", port: 3000 }),
      row({ path: "/v1/orders/*" }),
    ]),
    card("api.acme.dev", [row({ path: "/v1/orders/*" })]),
  ]
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)

  const acme = await hostRow("acme.dev")
  const blocked = within(acme).getByRole("button", { name: "Detach" })
  expect(blocked).toBeDisabled()
  await userEvent.hover(blocked.parentElement!)
  expect(
    await screen.findByText("Move or delete its 2 routes first.")
  ).toBeInTheDocument()

  // FARP routes don't hold a hostname: they land wherever FARP is told.
  const api = await hostRow("api.acme.dev")
  await userEvent.click(within(api).getByRole("button", { name: "Detach" }))
  const confirm = await screen.findByRole("alertdialog")
  expect(confirm).toHaveTextContent(
    "Detach api.acme.dev? Loomplane removes its DNS record and stops serving it."
  )
  const action = within(confirm).getByRole("button", { name: "Detach" })
  await vi.waitFor(() => expect(action).toBeEnabled())
  await userEvent.click(action)
  await vi.waitFor(() =>
    expect(posts(writes)).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/p/domain-routes/dr_api.acme.dev/remove",
        body: { revision: 7 },
      },
    ])
  )
  await vi.waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
})

test("detach says so when the domain route can't be found", async () => {
  const b = board()
  b.hosts = [card("api.acme.dev", [row({ path: "/v1/orders/*" })])]
  const writes = serve({ board: b, domainRoutes: [] })
  mount(<DomainsPanel {...panelProps()} />)
  await userEvent.click(
    within(await hostRow("api.acme.dev")).getByRole("button", {
      name: "Detach",
    })
  )
  const confirm = await screen.findByRole("alertdialog")
  expect(await within(confirm).findByRole("alert")).toHaveTextContent(
    "Loomplane couldn't read this domain's route. Try again in a moment."
  )
  expect(within(confirm).getByRole("button", { name: "Detach" })).toBeDisabled()
  expect(posts(writes)).toEqual([])
})

test("attach previews then applies a gateway route", async () => {
  const b = board()
  b.gateway.attach.cloud_id = "cloud_1"
  const writes = serve({ board: b, domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Attach a domain" })
  )
  const dialog = await screen.findByRole("dialog", { name: "Attach a domain" })
  expect(
    within(dialog).getByText(
      "Loomplane adds the DNS record if the zone is connected, then issues a certificate. The route starts serving once both check out."
    )
  ).toBeVisible()
  // Wildcards are allowed on a gateway.
  expect(
    await within(dialog).findByRole("button", { name: "Every subdomain" })
  ).toBeVisible()
  expect(within(dialog).queryByRole("button", { name: "Attach" })).toBeNull()
  await userEvent.type(
    within(dialog).getByRole("textbox", { name: "Subdomain" }),
    "docs"
  )
  await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }))

  const record = await within(dialog).findByRole("group", {
    name: "DNS record",
  })
  expect(record).toHaveTextContent("CNAME")
  expect(record).toHaveTextContent("docs.acme.dev")
  expect(record).toHaveTextContent("edge-1.loomplane.net")
  await userEvent.click(within(dialog).getByRole("button", { name: "Attach" }))
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())

  expect(posts(writes)).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/domain-routes/preview",
      body: {
        zone_id: "zone_dev",
        cloud_id: "cloud_1",
        environment_id: "env_prod",
        service: "edge--gateway",
        hostname: "docs.acme.dev",
        port: 8080,
        blocked_path_prefixes: ["/__farp", "/_farp", "/admin"],
      },
    },
    {
      method: "POST",
      path: "/api/v1/projects/p/route-operations/routeop_1/apply",
      body: { review: "review-token" },
    },
  ])
})

test("a failed attach says why and keeps the dialog open", async () => {
  const writes = serve({
    board: board(),
    domainRoutes: domainRoutes(),
    write: (w) =>
      w.path.endsWith("/apply")
        ? json(
            operation(
              "routeop_1",
              "failed",
              "The DNS provider refused the record."
            )
          )
        : undefined,
  })
  mount(<DomainsPanel {...panelProps()} />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Attach a domain" })
  )
  const dialog = await screen.findByRole("dialog", { name: "Attach a domain" })
  await userEvent.type(
    await within(dialog).findByRole("textbox", { name: "Subdomain" }),
    "docs"
  )
  await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }))
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Attach" })
  )
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    "The DNS provider refused the record."
  )
  expect(screen.getByRole("dialog", { name: "Attach a domain" })).toBeVisible()
  expect(posts(writes).map((w) => w.path)).toEqual([
    "/api/v1/projects/p/domain-routes/preview",
    "/api/v1/projects/p/route-operations/routeop_1/apply",
  ])
})

test("changing the hostname after a preview previews again", async () => {
  serve({ board: board(), domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Attach a domain" })
  )
  const dialog = await screen.findByRole("dialog", { name: "Attach a domain" })
  const subdomain = await within(dialog).findByRole("textbox", {
    name: "Subdomain",
  })
  await userEvent.type(subdomain, "docs")
  await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }))
  expect(
    await within(dialog).findByRole("group", { name: "DNS record" })
  ).toBeVisible()
  await userEvent.type(subdomain, "2")
  expect(within(dialog).queryByRole("group", { name: "DNS record" })).toBeNull()
  expect(within(dialog).getByRole("button", { name: "Preview" })).toBeVisible()
})

test("domains empty state and no-zone state", async () => {
  const empty = board()
  empty.hosts = [card("", [row({ path: "/v1/orders/*" })], { farp_home: true })]
  serve({ board: empty })
  const view = mount(<DomainsPanel {...panelProps()} />)
  expect(await screen.findByText("No domains yet")).toBeVisible()
  expect(
    screen.getByText(
      "Octopus answers on the target's private network until you attach one. Pick a hostname from your organization's zones."
    )
  ).toBeVisible()
  expect(
    screen.getAllByRole("button", { name: "Attach a domain" })
  ).toHaveLength(1)
  view.unmount()
  forgeClient.clear()

  serve({ board: empty, projectDomains: [] })
  mount(<DomainsPanel {...panelProps()} />)
  expect(
    await screen.findByText(
      /Your organization has no zones this project can use\./
    )
  ).toHaveTextContent(
    "Your organization has no zones this project can use. Add one on the Domains page."
  )
  expect(screen.getByRole("link", { name: "Domains page" })).toHaveAttribute(
    "href",
    "/domains"
  )
  expect(screen.queryByRole("button", { name: "Attach a domain" })).toBeNull()
})

test("a zone that isn't verified can't be picked, so it counts as none", async () => {
  const empty = board()
  empty.hosts = []
  serve({
    board: empty,
    projectDomains: [
      {
        zone_id: "zone_new",
        name: "acme.shop",
        mode: "external",
        verified: false,
        wildcards: false,
        hostnames: [],
      },
    ],
  })
  mount(<DomainsPanel {...panelProps()} />)
  expect(
    await screen.findByText(
      /Your organization has no zones this project can use\./
    )
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Attach a domain" })).toBeNull()
})

test("hides domain writes without infrastructure:modify", async () => {
  permissions.can = (p) => p !== "infrastructure:modify"
  serve({ board: board(), domainRoutes: domainRoutes() })
  mount(<DomainsPanel {...panelProps()} />)
  const api = await hostRow("api.acme.dev")
  expect(
    within(api).getByRole("switch", { name: "FARP routes land here" })
  ).toHaveAttribute("aria-disabled", "true")
  expect(within(api).queryByRole("button", { name: "Detach" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Attach a domain" })).toBeNull()
  expect(within(api).getByRole("link", { name: "Zone settings" })).toBeVisible()
})

test("loading, denied and failed requests stay distinct on each tab", async () => {
  serve({ boardPending: true })
  const view = mount(<DomainsPanel {...panelProps()} />)
  expect(await screen.findByLabelText("Loading domains")).toBeVisible()
  view.unmount()
  forgeClient.clear()

  serve({ boardStatus: 403 })
  const denied = mount(<DomainsPanel {...panelProps()} />)
  expect(
    await screen.findByText("You can't see this gateway's domains")
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  denied.unmount()
  forgeClient.clear()

  serve({ boardStatus: 500 })
  mount(<DomainsPanel {...panelProps()} />)
  expect(await screen.findByRole("button", { name: "Retry" })).toBeVisible()
  expect(screen.getByText("Something went wrong.")).toBeVisible()
  expect(screen.queryByText("You can't see this gateway's domains")).toBeNull()
})
