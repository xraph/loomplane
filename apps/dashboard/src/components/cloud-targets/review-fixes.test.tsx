import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { Pools } from "./pools"
import { Servers } from "./servers"
import { Domains } from "./domains"
const permissions = vi.hoisted(() => ({ allowed: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => permissions.allowed }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    to,
    search,
    children,
  }: {
    to: string
    search?: Record<string, string>
    children: React.ReactNode
  }) => (
    <a href={`${to}${search ? `?${new URLSearchParams(search)}` : ""}`}>
      {children}
    </a>
  ),
}))
afterEach(() => {
  permissions.allowed = true
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
const cloud = { id: "c", name: "Shared", revision: 1 }
const pool = {
  id: "pool",
  name: "Pool",
  revision: 1,
  members: [
    {
      server_id: "host",
      draining: false,
      cpu_millis: 2000,
      memory_bytes: 4294967296,
    },
  ],
}
const host = {
  id: "host",
  name: "Host",
  status: "ready",
  ownership: "adopted",
  health: { effective_cpu_millis: 2000, effective_memory_bytes: 4294967296 },
}
function mount(children: React.ReactNode) {
  return render(
    <ClientProvider client={forgeClient}>{children}</ClientProvider>
  )
}
function fixture(failure = "") {
  let recovered = false
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (failure && path.endsWith(`/${failure}`))
        return Response.json(
          { message: `${failure} unavailable` },
          { status: failure === "projects" ? 403 : 500 }
        )
      if (path.endsWith("/preview-move") || path.endsWith("/accept-move"))
        return Response.json({
          review: "saved-move",
          input: JSON.parse(String(init?.body)),
          placement: {},
          source_server_id: "source",
        })
      if (path.endsWith("/preview-recovery") || path.endsWith("/recover")) {
        if (path.endsWith("/recover")) recovered = true
        return Response.json({
          server_id: "host",
          phase: recovered ? "released" : "failed",
          active: false,
          started: false,
          plan_id: "old-plan",
          service: "api",
          review: "saved-recovery",
          storage_retained: false,
        })
      }
      if (path.endsWith("/usage"))
        return Response.json({
          server_id: "host",
          reserved_cpu_millis: recovered ? 0 : 1000,
          reserved_memory_bytes: 0,
          retained_storage: [],
          workloads: recovered
            ? []
            : [
                {
                  project_id: "p",
                  environment_id: "env-id",
                  service: "api",
                  phase: "failed-reservation",
                  active: false,
                  plan_id: "old-plan",
                  cpu_millis: 1000,
                  memory_bytes: 0,
                },
              ],
        })
      if (path.endsWith("/pools")) return Response.json({ pools: [pool] })
      if (path.endsWith("/servers")) return Response.json({ servers: [host] })
      if (path.endsWith("/projects"))
        return Response.json({
          projects: [{ id: "p", name: "Project", repositories: [] }],
        })
      if (path.endsWith("/environments"))
        return Response.json({
          environments: [{ id: "env-id", name: "production" }],
        })
      if (path.endsWith("/connections"))
        return Response.json({ connections: [] })
      if (path.endsWith("/providers")) return Response.json({ providers: [] })
      if (path.endsWith("/dns-zones"))
        return Response.json({
          zones: [
            {
              id: "zone",
              name: "example.test",
              mode: "external",
              project_ids: ["p"],
              provider_zone: {},
            },
          ],
        })
      return Response.json({})
    }
  )
  return {
    restoreReads: () => {
      failure = ""
    },
  }
}
it("stages a 15-minute move and links to the existing deployment planner with the selected environment name", async () => {
  fixture()
  const user = userEvent.setup()
  mount(<Pools cloud={cloud as never} servers={[host] as never} />)
  await user.click(
    await screen.findByRole("button", { name: "Review service move" })
  )
  await user.click(screen.getByRole("combobox", { name: "Move project" }))
  await user.click(await screen.findByRole("option", { name: /Project/ }))
  await user.click(
    await screen.findByRole("combobox", { name: "Route environment" })
  )
  await user.click(await screen.findByRole("option", { name: /production/ }))
  await user.type(screen.getByRole("textbox", { name: "Move service" }), "api")
  await user.click(screen.getByRole("combobox", { name: "Destination server" }))
  await user.click(await screen.findByRole("option", { name: /Host/ }))
  await user.click(screen.getByRole("button", { name: "Preview move" }))
  await user.click(
    await screen.findByRole("button", { name: "Accept reviewed move" })
  )
  expect(
    await screen.findByText(/15 minutes after acceptance/)
  ).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Create deployment plan" })
  ).toHaveAttribute("href", "/deployments/new?project=p&environment=production")
  expect(screen.getByText(/No deployment has started/)).toBeInTheDocument()
})
it("refreshes cached reservation usage after recovery before reopening the server", async () => {
  fixture()
  const user = userEvent.setup()
  mount(<Servers cloud={cloud as never} />)
  await user.click(await screen.findByRole("button", { name: "Inspect Host" }))
  await user.click(
    await screen.findByRole("button", { name: "Review reservation recovery" })
  )
  await user.click(screen.getByRole("button", { name: "Preview recovery" }))
  await user.click(
    await screen.findByRole("button", { name: "Apply reviewed recovery" })
  )
  expect(await screen.findByText("Recovery recorded.")).toBeInTheDocument()
  await user.keyboard("{Escape}")
  await user.click(screen.getByRole("button", { name: "Inspect Host" }))
  await waitFor(() =>
    expect(screen.queryByText(/failed-reservation/)).not.toBeInTheDocument()
  )
  expect(
    screen.queryByRole("button", { name: "Review reservation recovery" })
  ).not.toBeInTheDocument()
  expect(screen.getAllByText(/reserved 0 mCPU/).length).toBeGreaterThan(0)
})
it.each(["projects", "providers", "connections"])(
  "shows failed %s reads and retry in zone setup",
  async (failure) => {
    fixture(failure)
    const user = userEvent.setup()
    mount(<Domains cloud={cloud as never} projectId="" />)
    await user.click(
      await screen.findByRole("button", { name: "Add DNS zone" })
    )
    if (failure !== "projects") {
      await user.click(screen.getByRole("combobox", { name: "DNS mode" }))
      await user.click(
        await screen.findByRole("option", { name: /Managed DNS/ })
      )
    }
    expect(
      await screen.findByText(new RegExp(`${failure} unavailable`, "i"))
    ).toBeInTheDocument()
    expect(
      screen.getAllByRole("button", { name: "Retry" }).length
    ).toBeGreaterThan(0)
    expect(screen.getByRole("button", { name: "Save DNS zone" })).toBeDisabled()
  }
)
it("shows denied project reads in move setup with retry", async () => {
  fixture("projects")
  const user = userEvent.setup()
  mount(<Pools cloud={cloud as never} servers={[host] as never} />)
  await user.click(
    await screen.findByRole("button", { name: "Review service move" })
  )
  expect(await screen.findByText(/projects unavailable/i)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
})
it("shows denied project reads in zone access with retry", async () => {
  fixture("projects")
  const user = userEvent.setup()
  mount(<Domains cloud={cloud as never} projectId="" />)
  await user.click(await screen.findByRole("button", { name: "Zone access" }))
  expect(await screen.findByText(/projects unavailable/i)).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Save zone access" })
  ).toBeDisabled()
})

it("distinguishes loading projects from a genuinely empty project list in zone setup", async () => {
  let finish!: (response: Response) => void
  const projects = new Promise<Response>((resolve) => {
    finish = resolve
  })
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("/projects")) return projects
    if (path.endsWith("/connections")) return Response.json({ connections: [] })
    if (path.endsWith("/providers")) return Response.json({ providers: [] })
    return Response.json({ zones: [] })
  })
  const user = userEvent.setup()
  mount(<Domains cloud={cloud as never} projectId="" />)
  await user.click(await screen.findByRole("button", { name: "Add DNS zone" }))
  expect(screen.getByText("Loading projects...")).toBeInTheDocument()
  expect(
    screen.queryByText("Create a project for this zone")
  ).not.toBeInTheDocument()
  finish(Response.json({ projects: [] }))
  expect(
    await screen.findByText("Create a project for this zone")
  ).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Save DNS zone" })).toBeDisabled()
})
it("retries a denied projects read and restores zone grant choices without reloading", async () => {
  const { restoreReads } = fixture("projects")
  const user = userEvent.setup()
  mount(<Domains cloud={cloud as never} projectId="" />)
  await user.click(await screen.findByRole("button", { name: "Add DNS zone" }))
  expect(
    await screen.findByText("Projects access is restricted.")
  ).toBeInTheDocument()
  restoreReads()
  await user.click(screen.getByRole("button", { name: "Retry" }))
  expect(
    await screen.findByRole("checkbox", { name: "Project" })
  ).toBeInTheDocument()
  expect(
    screen.queryByText("Projects access is restricted.")
  ).not.toBeInTheDocument()
})

it.each([
  [
    "Servers",
    "No servers connected",
    "Connect existing server",
    "Connect existing server",
  ],
  ["Domains", "No DNS zones", "Add DNS zone", "Add DNS zone"],
  ["Routes", "No routes for this project", "Add route", "Add route"],
])(
  "opens the existing %s dialog from its shared empty state",
  async (kind, title, action, dialog) => {
    vi.stubGlobal("fetch", async () =>
      Response.json({
        servers: [],
        pools: [],
        zones: [],
        routes: [],
        projects: [],
        connections: [],
        providers: [],
        environments: [],
      })
    )
    mount(
      kind === "Servers" ? (
        <Servers cloud={cloud as never} />
      ) : (
        <Domains
          cloud={cloud as never}
          projectId={kind === "Routes" ? "p" : ""}
        />
      )
    )
    const state = (await screen.findByText(title)).closest(
      '[data-slot="empty"]'
    )!
    await userEvent
      .setup()
      .click(within(state as HTMLElement).getByRole("button", { name: action }))
    expect(
      await screen.findByRole("dialog", { name: dialog })
    ).toBeInTheDocument()
  }
)
it("keeps denied server and route empty states explanatory without actions", async () => {
  permissions.allowed = false
  vi.stubGlobal("fetch", async () => Response.json({ routes: [], zones: [] }))
  mount(
    <>
      <Servers cloud={cloud as never} />
      <Domains cloud={cloud as never} projectId="p" />
    </>
  )
  for (const title of [
    "Server inventory is restricted",
    "No routes for this project",
  ]) {
    const state = (await screen.findByText(title)).closest(
      '[data-slot="empty"]'
    )!
    expect(
      within(state as HTMLElement).queryByRole("button")
    ).not.toBeInTheDocument()
  }
  expect(
    screen.queryByRole("button", { name: "Add DNS zone" })
  ).not.toBeInTheDocument()
})
