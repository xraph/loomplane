import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { RoutesPanel } from "@/components/octopus/routes-panel"
import {
  card,
  here,
  json,
  mockBoard,
  mount,
  panelProps,
  serve,
  type Write,
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

const routesPath =
  "/api/v1/projects/p/environments/env_prod/octopus/edge/routes"

/** The board from the mock, with the auth route's id easy to read. */
type Json = Record<string, unknown>
type TestBoard = Json & {
  gateway: Json & { attach: Json }
  hosts: (Json & { routes: Json[] })[]
}

function board(): TestBoard {
  const b = mockBoard() as TestBoard
  for (const host of b.hosts)
    for (const route of host.routes)
      if (route.path === "/auth/*") route.route_id = "rt_auth"
  return b
}

async function openAdd() {
  await userEvent.click(
    await screen.findByRole("button", { name: "Add route" })
  )
  return screen.findByRole("dialog", { name: "Add a route to edge" })
}

async function pick(dialog: HTMLElement, field: string, option: string) {
  await userEvent.click(within(dialog).getByRole("combobox", { name: field }))
  await userEvent.click(await screen.findByRole("option", { name: option }))
}

async function typePath(dialog: HTMLElement, path: string) {
  const input = within(dialog).getByRole("textbox", { name: "Path" })
  await userEvent.clear(input)
  // userEvent reads braces and brackets as keys; a path has neither.
  await userEvent.type(input, path)
}

const posts = (writes: Write[]) => writes.filter((w) => w.method === "POST")

test("adds a route with methods and strip prefix", async () => {
  let current: Json = board()
  const writes = serve({
    board: () => current,
    write: (w) => {
      if (!w.path.endsWith("/octopus/edge/routes")) return undefined
      const next = board()
      next.hosts[1].routes.push(
        here({ path: "/login/*", service: "auth", port: 9000, route_id: "x" })
      )
      current = next
      return undefined
    },
  })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  expect(
    within(dialog).getByText(
      "Send requests for a domain and path to a service in acme-shop / production. Loomplane pushes the route to Octopus without a redeploy."
    )
  ).toBeVisible()

  await pick(dialog, "Domain", "api.acme.dev")
  await typePath(dialog, "/login/*")
  expect(
    within(dialog).getByRole("textbox", { name: "Path" })
  ).toHaveAccessibleDescription("End with /* to match everything under it.")
  expect(
    within(dialog).getByText("· any method until you pick some")
  ).toBeVisible()
  const get = within(dialog).getByRole("button", { name: "GET" })
  await userEvent.click(get)
  expect(get).toHaveAttribute("aria-pressed", "true")
  expect(
    within(dialog).queryByText("· any method until you pick some")
  ).toBeNull()
  await userEvent.click(get)
  expect(get).toHaveAttribute("aria-pressed", "false")
  expect(
    within(dialog)
      .getAllByRole("button")
      .map((b) => b.textContent)
      .filter((t) => /^[A-Z]+$/.test(t ?? ""))
  ).toEqual(["GET", "POST", "PUT", "PATCH", "DELETE"])

  await pick(dialog, "Send to", "auth · port 9000")
  const strip = within(dialog).getByRole("checkbox", {
    name: "Strip /login before forwarding",
  })
  expect(strip).not.toBeChecked()
  await userEvent.click(strip)
  expect(strip).toBeChecked()

  const example = within(dialog).getByRole("group", { name: "Example request" })
  expect(example).toHaveTextContent("https://api.acme.dev/login/login")
  expect(example).toHaveTextContent("→ auth:9000/login")

  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(posts(writes)).toEqual([
    {
      method: "POST",
      path: routesPath,
      body: {
        hostname: "api.acme.dev",
        path: "/login/*",
        methods: [],
        service: "auth",
        port: 9000,
        strip_prefix: true,
        note: "",
      },
    },
  ])
  // The board is read again and shows the new route.
  const api = await screen.findByRole("region", { name: "api.acme.dev" })
  expect(
    await within(api).findByRole("row", { name: /\/login\/\*/ })
  ).toBeVisible()
  expect(toasts.toast).not.toHaveBeenCalled()
})

test("the strip box is off for the root and the example uses the private URL for any host", async () => {
  serve({ board: board() })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await pick(dialog, "Domain", "Any hostname")
  await typePath(dialog, "/")
  expect(
    within(dialog).getByRole("checkbox", { name: "Strip / before forwarding" })
  ).toHaveAttribute("aria-disabled", "true")
  await pick(dialog, "Send to", "web · port 3000")
  const example = within(dialog).getByRole("group", { name: "Example request" })
  expect(example).toHaveTextContent("http://edge--gateway:8080/")
  expect(example).toHaveTextContent("→ web:3000/")
  // A path the server would refuse says why before anything is sent.
  await typePath(dialog, "/auth/")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  expect(
    within(dialog).getByRole("textbox", { name: "Path" })
  ).toHaveAccessibleDescription(
    /Drop the trailing slash, or end with \/\* to match everything under it\./
  )
})

test("warns about a FARP collision while typing", async () => {
  serve({ board: board() })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await pick(dialog, "Domain", "api.acme.dev")
  await typePath(dialog, "/v1/orders")
  expect(within(dialog).queryByRole("note")).toBeNull()
  await userEvent.type(
    within(dialog).getByRole("textbox", { name: "Path" }),
    "/*"
  )
  const note = within(dialog).getByRole("note")
  expect(note).toHaveTextContent(
    "FARP also publishes /v1/orders/* on this domain from api. Your route takes priority while it exists, and the FARP route comes back if you delete it."
  )
  expect(within(note).getByText("api").tagName).toBe("B")
  // The same path on another domain doesn't collide.
  await pick(dialog, "Domain", "acme.dev")
  expect(within(dialog).queryByRole("note")).toBeNull()
})

test("attaches another domain before adding the route", async () => {
  const b = board()
  b.gateway.attach.cloud_id = "cloud_1"
  const writes = serve({ board: b })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await pick(dialog, "Domain", "Attach another domain…")
  expect(
    within(dialog).getByText(
      "Loomplane adds the DNS record if the zone is connected, then issues a certificate. The route starts serving once both check out."
    )
  ).toBeVisible()
  await userEvent.type(
    await within(dialog).findByRole("textbox", { name: "Subdomain" }),
    "docs"
  )
  expect(within(dialog).getByTestId("resulting-hostname")).toHaveTextContent(
    "docs.acme.dev"
  )
  await typePath(dialog, "/*")
  await pick(dialog, "Send to", "web · port 3000")
  expect(
    within(dialog).getByRole("group", { name: "Example request" })
  ).toHaveTextContent("https://docs.acme.dev/login")

  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
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
    {
      method: "POST",
      path: routesPath,
      body: {
        hostname: "docs.acme.dev",
        path: "/*",
        methods: [],
        service: "web",
        port: 3000,
        strip_prefix: false,
        note: "",
      },
    },
  ])
})

test("a refused route after attaching shows why and doesn't attach twice", async () => {
  let refuse = true
  const writes = serve({
    board: board(),
    write: (w) =>
      w.path === routesPath && refuse
        ? json({ message: "Attach docs.acme.dev to edge first." }, 422)
        : undefined,
  })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await pick(dialog, "Domain", "Attach another domain…")
  await userEvent.type(
    await within(dialog).findByRole("textbox", { name: "Subdomain" }),
    "docs"
  )
  await typePath(dialog, "/*")
  await pick(dialog, "Send to", "web · port 3000")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  expect(
    await within(dialog).findByText("Attach docs.acme.dev to edge first.")
  ).toBeVisible()
  expect(
    within(dialog).getByRole("group", { name: "Domain" })
  ).toHaveTextContent("Attach docs.acme.dev to edge first.")

  refuse = false
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(posts(writes).map((w) => w.path)).toEqual([
    "/api/v1/projects/p/domain-routes/preview",
    "/api/v1/projects/p/route-operations/routeop_1/apply",
    routesPath,
    routesPath,
  ])
})

test("override prefills from the FARP row", async () => {
  const writes = serve({ board: board() })
  mount(<RoutesPanel {...panelProps()} />)
  const api = await screen.findByRole("region", { name: "api.acme.dev" })
  await userEvent.click(
    within(
      within(api).getByRole("row", { name: /\/v1\/orders\/\*/ })
    ).getByRole("button", { name: "Override" })
  )
  const dialog = await screen.findByRole("dialog", {
    name: "Add a route to edge",
  })
  expect(
    within(dialog).getByRole("combobox", { name: "Domain" })
  ).toHaveTextContent("api.acme.dev")
  expect(within(dialog).getByRole("textbox", { name: "Path" })).toHaveValue(
    "/v1/orders/*"
  )
  expect(within(dialog).getByRole("button", { name: "GET" })).toHaveAttribute(
    "aria-pressed",
    "true"
  )
  expect(within(dialog).getByRole("button", { name: "POST" })).toHaveAttribute(
    "aria-pressed",
    "true"
  )
  expect(within(dialog).getByRole("button", { name: "PUT" })).toHaveAttribute(
    "aria-pressed",
    "false"
  )
  expect(
    within(dialog).getByRole("combobox", { name: "Send to" })
  ).toHaveTextContent("api · port 8080")
  // A FARP row's note comes from its schema, so the override starts blank.
  expect(within(dialog).getByRole("textbox", { name: "Note" })).toHaveValue("")
  expect(within(dialog).getByRole("note")).toHaveTextContent(
    "FARP also publishes /v1/orders/* on this domain from api."
  )
  expect(
    within(dialog).queryByRole("button", { name: "Delete route" })
  ).toBeNull()

  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(posts(writes)[0].body).toEqual({
    hostname: "api.acme.dev",
    path: "/v1/orders/*",
    methods: ["GET", "POST"],
    service: "api",
    port: 8080,
    strip_prefix: false,
    note: "",
  })
})

test("edit saves with the revision and delete confirms", async () => {
  const writes = serve({ board: board() })
  mount(<RoutesPanel {...panelProps()} />)
  const api = await screen.findByRole("region", { name: "api.acme.dev" })
  const editAuth = async () => {
    await userEvent.click(
      within(within(api).getByRole("row", { name: /\/auth\/\*/ })).getByRole(
        "button",
        { name: "Edit" }
      )
    )
    return screen.findByRole("dialog", { name: "Edit a route on edge" })
  }

  let dialog = await editAuth()
  expect(
    within(dialog).getByRole("checkbox", {
      name: "Strip /auth before forwarding",
    })
  ).toBeChecked()
  await userEvent.type(
    within(dialog).getByRole("textbox", { name: "Note" }),
    "Sign-in"
  )
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Save route" })
  )
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(posts(writes)).toEqual([
    {
      method: "POST",
      path: `${routesPath}/rt_auth/update`,
      body: {
        hostname: "api.acme.dev",
        path: "/auth/*",
        methods: [],
        service: "auth",
        port: 9000,
        strip_prefix: true,
        note: "Sign-in",
        revision: 3,
      },
    },
  ])

  dialog = await editAuth()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Delete route" })
  )
  const confirm = await screen.findByRole("alertdialog")
  expect(confirm).toHaveTextContent(
    "Delete /auth/* on api.acme.dev? If FARP publishes the same path, its route comes back."
  )
  await userEvent.click(
    within(confirm).getByRole("button", { name: "Delete route" })
  )
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(posts(writes).at(-1)).toEqual({
    method: "POST",
    path: `${routesPath}/rt_auth/remove`,
    body: { revision: 3 },
  })
})

test("shows server validation inline", async () => {
  serve({
    board: board(),
    write: (w) =>
      w.path === routesPath
        ? json(
            {
              message:
                "A route for /auth/* on api.acme.dev already exists. Edit it instead.",
            },
            422
          )
        : undefined,
  })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await pick(dialog, "Domain", "api.acme.dev")
  await typePath(dialog, "/auth/*")
  await pick(dialog, "Send to", "auth · port 9000")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  const path = within(dialog).getByRole("textbox", { name: "Path" })
  await vi.waitFor(() =>
    expect(path).toHaveAccessibleDescription(
      /A route for \/auth\/\* on api\.acme\.dev already exists\. Edit it instead\./
    )
  )
  expect(path).toHaveAttribute("aria-invalid", "true")
  expect(screen.getByRole("dialog")).toBeVisible()
})

test("a server message that names no field shows above the buttons", async () => {
  serve({
    board: board(),
    write: (w) =>
      w.path === routesPath
        ? json({ message: "Something else went wrong." }, 409)
        : undefined,
  })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await typePath(dialog, "/docs/*")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    "Something else went wrong."
  )
})

test("reports a pending push without failing", async () => {
  const message =
    "Octopus isn't answering on its private port. Loomplane keeps the route and pushes it within 30 seconds."
  serve({
    board: board(),
    write: (w) =>
      w.path === routesPath
        ? json(
            {
              route: { id: "rt_new", revision: 1 },
              sync: {
                state: "pending",
                message,
                generation: 5,
                restarted: false,
                restored: 0,
              },
            },
            201
          )
        : undefined,
  })
  mount(<RoutesPanel {...panelProps()} />)
  const dialog = await openAdd()
  await typePath(dialog, "/docs/*")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add route" })
  )
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(toasts.toast).toHaveBeenCalledWith(`Saved. ${message}`)
  expect(toasts.toast.error).not.toHaveBeenCalled()
})

test("routes a service from the unrouted line", async () => {
  serve({ board: board() })
  mount(<RoutesPanel {...panelProps()} />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Route admin" })
  )
  const dialog = await screen.findByRole("dialog", {
    name: "Add a route to edge",
  })
  expect(
    within(dialog).getByRole("combobox", { name: "Send to" })
  ).toHaveTextContent("admin · port 4000")
  expect(
    within(dialog)
      .getAllByRole("button")
      .some((b) => b.textContent === "Cancel")
  ).toBe(true)
  await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
})

test("a host card's Route button starts on that domain", async () => {
  const b = board()
  b.hosts.push(card("docs.acme.dev", [], { farp_home: false }))
  serve({ board: b })
  mount(<RoutesPanel {...panelProps()} />)
  const acme = await screen.findByRole("region", { name: "*.acme.dev" })
  await userEvent.click(within(acme).getByRole("button", { name: "Route" }))
  const dialog = await screen.findByRole("dialog", {
    name: "Add a route to edge",
  })
  expect(
    within(dialog).getByRole("combobox", { name: "Domain" })
  ).toHaveTextContent("*.acme.dev")
  await userEvent.click(
    within(dialog).getByRole("combobox", { name: "Domain" })
  )
  expect(
    (await screen.findAllByRole("option")).map((o) => o.textContent)
  ).toEqual([
    "acme.dev",
    "api.acme.dev",
    "*.acme.dev",
    "docs.acme.dev",
    "Any hostname",
    "Attach another domain…",
  ])
})
