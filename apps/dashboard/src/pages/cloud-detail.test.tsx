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

import { CloudDetail } from "./cloud-detail"

// Tests switch between an organization manager and a project member.
let manager = true
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    can: (p: string) => manager || p !== "organization_cloud:manage",
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => Promise.resolve(),
}))
afterEach(() => {
  manager = true
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const cloud = {
  id: "c",
  name: "Production",
  org_id: "org",
  project_id: "",
  revision: 1,
  created_at: "2026-10-20T00:00:00Z",
  updated_at: "2026-10-20T00:00:00Z",
}
const target = (id: string, provider: string, connection: string) => ({
  id,
  name: id,
  display_name: id[0]!.toUpperCase() + id.slice(1),
  cloud_id: "c",
  provider,
  connection_id: connection,
  region: "",
  revision: 1,
})

const usage = {
  cloud_id: "c",
  environments: [
    {
      project_id: "web",
      project_name: "Web app",
      environment_id: "prod",
      environment_name: "production",
      assigned: true,
      placements: [
        { target_id: "frontend", services: ["site", "docs"] },
        { target_id: "api", services: ["api"] },
      ],
    },
  ],
}

const none = { servers: 0, cpu_millis: 0, memory_bytes: 0, storage_bytes: 0 }
const unknown = {
  servers: null,
  cpu_millis: null,
  memory_bytes: null,
  storage_bytes: null,
}
const capacityUsage = {
  cloud_id: "c",
  accounts: [],
  records: [],
  provisioned: none,
  pending: none,
  reserved: none,
  observed: unknown,
  system_reserve: none,
  allocatable: unknown,
  headroom: none,
  observed_storage_bytes: null,
  checked_at: "2026-10-20T00:00:00Z",
  fresh: true,
  gpus: [],
  frontends: [],
}

function fixture({
  granted = false,
  inUse = false,
  noTargets = false,
  unused = false,
  profile = {} as Record<string, unknown>,
  syncs = [] as unknown[],
} = {}) {
  const writes: { method: string; path: string; body: unknown }[] = []
  const reads: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const path = url.pathname
      const method = init?.method ?? "GET"
      if (method === "GET") reads.push(path + url.search)
      if (method !== "GET") {
        const sent = JSON.parse(String(init?.body ?? "{}"))
        writes.push({ method, path, body: sent })
        if (path.endsWith("/capacity-policy"))
          return Response.json({
            policy: { ...sent.policy, revision: 1 },
            automatic_expansion: false,
          })
        if (path.endsWith("/default-placement"))
          return inUse
            ? Response.json(
                { message: "an environment uses this Cloud" },
                { status: 409 }
              )
            : Response.json({
                ...cloud,
                revision: 2,
                default_placement: sent.default_placement,
              })
        return Response.json({
          cloud_id: "c",
          project_id: "web",
          target_ids: ["frontend"],
          revision: 2,
          revoked: false,
        })
      }
      let body: unknown = {}
      if (path === "/api/v1/clouds/c") body = { ...cloud, ...profile }
      if (path === "/api/v1/projects/web/clouds")
        body = { clouds: [{ ...cloud, ...profile }] }
      if (path.endsWith("/usage"))
        body = unused ? { cloud_id: "c", environments: [] } : usage
      if (path === "/api/v1/clouds/syncs" || path.endsWith("/cloud-syncs"))
        body = { syncs }
      if (path === "/api/v1/clouds/c/servers") body = { servers: [] }
      if (path === "/api/v1/clouds/c/pools")
        body = {
          pools: [
            {
              id: "web",
              name: "web",
              cloud_id: "c",
              org_id: "org",
              revision: 1,
              members: [],
            },
          ],
        }
      if (path === "/api/v1/clouds/c/capacity-policy")
        body = {
          automatic_expansion: false,
          policy: {
            org_id: "org",
            cloud_id: "c",
            revision: 0,
            enabled: false,
            require_expansion_approval: true,
            limits: {
              servers: 0,
              cpu_millis: 0,
              memory_bytes: 0,
              storage_bytes: 0,
            },
            pools: [],
            frontend_limits: [],
          },
        }
      if (path === "/api/v1/clouds/c/expansions") body = { expansions: [] }
      if (path === "/api/v1/clouds/c/capacity-usage") body = capacityUsage
      if (path === "/api/v1/clouds/c/operations") body = { operations: [] }
      if (path === "/api/v1/members") body = { members: [] }
      if (path.endsWith("/environments")) body = { environments: [] }
      if (path === "/api/v1/providers")
        body = {
          providers: [
            {
              name: "digitalocean",
              cloud: "digitalocean",
              title: "DigitalOcean App Platform",
              available: true,
              runs: ["service"],
              regions: ["nyc", "ams"],
            },
            {
              name: "vercel",
              cloud: "vercel",
              title: "Vercel",
              available: true,
              runs: ["service"],
            },
          ],
        }
      if (path.endsWith("/clouds/c/targets") && noTargets)
        body = { targets: [] }
      else if (path.endsWith("/clouds/c/targets"))
        body = {
          targets: [
            target("frontend", "vercel", "v"),
            target("api", "digitalocean", "do-1"),
            target("worker", "digitalocean", "do-2"),
          ],
        }
      if (path === "/api/v1/clouds/c/projects")
        body = {
          grants: granted
            ? [
                {
                  cloud_id: "c",
                  project_id: "web",
                  target_ids: ["frontend", "api"],
                  revision: 1,
                  revoked: false,
                },
              ]
            : [],
        }
      if (path === "/api/v1/projects")
        body = {
          projects: [
            { id: "web", name: "Web app", icon: "globe", color: "blue" },
            { id: "jobs", name: "Jobs", icon: "box", color: "green" },
          ],
        }
      if (path === "/api/v1/connections")
        body = {
          connections: [
            {
              id: "v",
              name: "Vercel team",
              cloud: "vercel",
              account: "team",
              status: "ready",
            },
            {
              id: "do-1",
              name: "DO US",
              cloud: "digitalocean",
              account: "us",
              status: "ready",
            },
            {
              id: "do-2",
              name: "DO EU",
              cloud: "digitalocean",
              account: "eu",
              status: "ready",
            },
          ],
        }
      return Response.json(body)
    }
  )
  return { writes, reads }
}

const show = (project?: string, tab?: "capacity") =>
  render(
    <ClientProvider client={forgeClient}>
      <CloudDetail id="c" project={project} tab={tab} />
    </ClientProvider>
  )
const openTab = async (name: string) =>
  userEvent.click(
    await screen.findByRole("tab", { name: new RegExp(`^${name}`) })
  )

it("shows where services go and how many run on each target", async () => {
  const { reads } = fixture({ granted: true })
  show()
  const where = await screen.findByRole("region", { name: "Where services go" })
  await waitFor(() => expect(where).toHaveTextContent("2 services"))
  expect(where).toHaveTextContent("1 service")
  expect(where).toHaveTextContent("0 services")
  expect(
    await screen.findByRole("region", { name: "Environments on this Cloud" })
  ).toHaveTextContent(/production.*Web app.*3 services.*In sync/)
  expect(screen.getByRole("banner")).toHaveTextContent("In sync")
  // History is asked for this Cloud only, not filtered from a page of all.
  expect(reads).toContain("/api/v1/clouds/syncs?cloud=c&limit=30")
})

it("names each change in words and shows what moved", async () => {
  fixture({
    syncs: [
      {
        id: "s1",
        cloud_id: "c",
        target_id: "api",
        status: "routing_pending",
        attempt: 1,
        created_at: new Date().toISOString(),
        before: { ...target("api", "vercel", "v") },
        after: { ...target("api", "digitalocean", "do-1"), region: "nyc" },
        environments: [
          {
            environment_id: "prod",
            environment: "production",
            status: "succeeded",
            external_routing: true,
            services: ["api"],
            summary: {},
          },
        ],
      },
    ],
  })
  show()
  expect(
    await screen.findByText(
      "Moved Api to DO US: waiting on routing in production"
    )
  ).toBeInTheDocument()
  await openTab("Changes")
  expect(await screen.findByText("Moved Api to DO US")).toBeInTheDocument()
  expect(
    screen.getByText("Vercel team · its default region")
  ).toBeInTheDocument()
  expect(screen.getByText("DO US · nyc")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Confirm routing" })).toBeDisabled()
})

it("shows a project member only the project's share, read-only", async () => {
  manager = false
  const { reads } = fixture()
  show("web")
  await screen.findByRole("region", { name: "Where services go" })
  expect(
    screen.queryByRole("tab", { name: /^Projects/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("tab", { name: /^Servers/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("tab", { name: /^Settings/ })
  ).not.toBeInTheDocument()
  expect(reads).toContain("/api/v1/projects/web/clouds/c/usage")
  expect(reads.some((r) => r.startsWith("/api/v1/clouds/c/operations"))).toBe(
    false
  )
  await openTab("Targets")
  expect(
    screen.queryByRole("button", { name: /^Edit/ })
  ).not.toBeInTheDocument()
})

it("shows an organization's Cloud with the projects allowed on each target", async () => {
  fixture({ granted: true })
  show()
  await openTab("Projects")
  const access = await screen.findByRole("region", {
    name: "Projects with access",
  })
  await waitFor(() => expect(access).toHaveTextContent("Web app"))
  // Web app may use frontend and api, not worker, and production chose it.
  expect(access).toHaveTextContent(
    /May use\s*frontend\s*api\s*Environments\s*production/
  )
  expect(access).not.toHaveTextContent("worker")
  expect(
    screen.getByRole("button", { name: "Edit access" })
  ).toBeInTheDocument()
})

it("gives a project access to only the targets you tick", async () => {
  const user = userEvent.setup()
  const { writes } = fixture({ unused: true })
  show()
  // Nothing uses the Cloud yet, so there is nothing to call in sync.
  await waitFor(() =>
    expect(screen.getByRole("banner")).toHaveTextContent("Not in use")
  )
  await openTab("Projects")
  expect(
    await screen.findByText("No project has access yet")
  ).toBeInTheDocument()
  await user.click(
    await screen.findByRole("button", { name: "Give a project access" })
  )
  await user.click(screen.getByRole("combobox", { name: "Project" }))
  await user.click(await screen.findByRole("option", { name: /Jobs/ }))
  // Every target starts ticked; leave worker out.
  await user.click(screen.getByRole("checkbox", { name: /Worker/ }))
  await user.click(screen.getByRole("button", { name: "Give access" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "PUT",
        path: "/api/v1/clouds/c/projects/jobs",
        body: { target_ids: ["frontend", "api"] },
      },
    ])
  )
})

it("disables target choices for whole Cloud access and all grant choices while saving", async () => {
  const user = userEvent.setup()
  fixture()
  const fetch = globalThis.fetch
  let finishSave!: (response: Response) => void
  const pendingSave = new Promise<Response>((resolve) => {
    finishSave = resolve
  })
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "PUT" && String(input).includes("/projects/"))
      return pendingSave
    return fetch(input, init)
  })
  show()
  await openTab("Projects")
  await user.click(
    await screen.findByRole("button", { name: "Give a project access" })
  )
  const wholeCloud = screen.getByRole("checkbox", { name: /Whole Cloud/ })
  const frontend = screen.getByRole("checkbox", { name: /Frontend/ })
  await user.click(wholeCloud)
  expect(frontend).toHaveAttribute("aria-disabled", "true")
  await user.click(frontend)
  expect(frontend).toBeChecked()
  await user.click(wholeCloud)
  expect(frontend).not.toHaveAttribute("aria-disabled", "true")
  await user.click(screen.getByRole("button", { name: "Give access" }))
  await waitFor(() =>
    expect(wholeCloud).toHaveAttribute("aria-disabled", "true")
  )
  expect(frontend).toHaveAttribute("aria-disabled", "true")
  expect(screen.getByRole("combobox", { name: "Project" })).toBeDisabled()
  finishSave(
    Response.json({
      cloud_id: "c",
      project_id: "web",
      target_ids: ["frontend", "api", "worker"],
      revision: 2,
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
})

it("sets a targetless Cloud's one provider and account", async () => {
  const user = userEvent.setup()
  const { writes } = fixture({ noTargets: true })
  show()
  await openTab("Targets")
  expect(
    await screen.findByText(/every service on it runs here/)
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Set default provider" }))
  await user.click(
    await screen.findByRole("radio", { name: /DigitalOcean App Platform/ })
  )
  await user.click(screen.getByRole("radio", { name: /DO EU/ }))
  await user.click(screen.getByRole("combobox", { name: "Default region" }))
  await user.click(await screen.findByRole("option", { name: /ams/ }))
  await user.click(screen.getByRole("button", { name: "Set default" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "PUT",
        path: "/api/v1/clouds/c/default-placement",
        body: {
          default_placement: {
            provider: "digitalocean",
            connection_id: "do-2",
            region: "ams",
          },
          revision: 1,
        },
      },
    ])
  )
  expect(await screen.findByText("DO EU")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Change" })).toBeInTheDocument()
})

it("explains why a default can't move while an environment uses the Cloud", async () => {
  const user = userEvent.setup()
  fixture({ inUse: true })
  show()
  await openTab("Targets")
  await user.click(
    await screen.findByRole("button", { name: "Set default provider" })
  )
  await user.click(
    await screen.findByRole("radio", { name: /DigitalOcean App Platform/ })
  )
  await user.click(screen.getByRole("radio", { name: /DO US/ }))
  await user.click(screen.getByRole("button", { name: "Set default" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /Unassign the Cloud from those environments first/
  )
})

it("reviews a default provider switch through the organization Cloud change contract", async () => {
  fixture({
    profile: {
      default_placement: { provider: "vercel", connection_id: "v", region: "" },
    },
  })
  const user = userEvent.setup()
  show()
  await openTab("Targets")
  await user.click(await screen.findByRole("button", { name: "Change" }))
  await user.click(
    await screen.findByRole("radio", { name: /DigitalOcean App Platform/ })
  )
  await user.click(screen.getByRole("radio", { name: /DO US/ }))
  expect(
    screen.getByRole("button", { name: "Review default switch" })
  ).toBeInTheDocument()
})

it("says pools won't grow on their own while automatic expansion is off, and saves the policy", async () => {
  const user = userEvent.setup()
  const { writes } = fixture()
  show()
  await openTab("Capacity")
  expect(
    await screen.findByText(
      "Automatic expansion isn't on for this Loomplane yet"
    )
  ).toBeInTheDocument()
  expect(
    screen.getByText(/nothing will appear here until it's turned on/)
  ).toBeInTheDocument()
  // A pool without a rule never grows.
  expect(
    screen.getByText("No rule, so this pool never grows.")
  ).toBeInTheDocument()
  await user.type(screen.getByRole("spinbutton", { name: "Servers" }), "3")
  await user.type(screen.getByRole("spinbutton", { name: "vCPU" }), "8")
  // A saved limit is enforced even while pools don't grow, so an empty one
  // is never saved as 0.
  await user.click(screen.getByRole("button", { name: "Save policy" }))
  expect(await screen.findByRole("alert")).toHaveTextContent("before you save")
  expect(writes).toEqual([])
  await user.type(screen.getByRole("spinbutton", { name: "Memory, GiB" }), "16")
  await user.type(
    screen.getByRole("spinbutton", { name: "Storage, GB" }),
    "200"
  )
  await user.click(screen.getByRole("button", { name: "Save policy" }))
  await waitFor(() =>
    expect(writes.at(-1)).toMatchObject({
      method: "POST",
      path: "/api/v1/clouds/c/capacity-policy",
      body: {
        expected_revision: 0,
        policy: {
          enabled: false,
          require_expansion_approval: true,
          limits: {
            servers: 3,
            cpu_millis: 8000,
            memory_bytes: 16 * 2 ** 30,
            storage_bytes: 200e9,
          },
          pools: [],
        },
      },
    })
  )
})

it("says which limits are missing before letting pools grow", async () => {
  const user = userEvent.setup()
  const { writes } = fixture()
  show()
  await openTab("Capacity")
  await user.click(
    await screen.findByRole("switch", { name: "Let pools on this Cloud grow" })
  )
  await user.type(screen.getByRole("spinbutton", { name: "Servers" }), "2")
  await user.click(screen.getByRole("button", { name: "Save policy" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "set the most this Cloud may hold for vCPU, memory, storage"
  )
  expect(writes).toEqual([])
})

// A paused deployment's "Review capacity" opens the Cloud on its Capacity
// tab, with ?project= set. A manager lands there with usage, the growth
// policy, the pools' rules with New scaling pool, and waiting servers.
it("opens on the Capacity tab with usage, the policy, pools and expansions", async () => {
  const { reads } = fixture()
  show("web", "capacity")
  expect(
    await screen.findByRole("tab", { name: /^Capacity/, selected: true })
  ).toBeInTheDocument()
  const usage = await screen.findByRole("region", { name: "Capacity usage" })
  const policy = await screen.findByRole("region", { name: "Growth policy" })
  const pools = screen.getByRole("region", { name: "How each pool grows" })
  const waiting = screen.getByRole("region", {
    name: "Servers waiting for approval",
  })
  const after = (a: Element, b: Element) =>
    !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
  expect(
    after(usage, policy) && after(policy, pools) && after(pools, waiting)
  ).toBe(true)
  expect(
    within(pools).getByRole("button", { name: "New scaling pool" })
  ).toBeInTheDocument()
  await waitFor(() => expect(reads).toContain("/api/v1/clouds/c/expansions"))
  // Capacity lives on its own tab; Servers doesn't mount a second copy.
  await openTab("Servers")
  expect(
    screen.queryByRole("region", { name: "Capacity usage" })
  ).not.toBeInTheDocument()
})
