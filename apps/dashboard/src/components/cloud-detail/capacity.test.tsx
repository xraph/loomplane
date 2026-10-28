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

import type { Connection, Pool } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"

import { Capacity } from "./capacity"

vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const GIB = 2 ** 30
const staleText =
  "This changed since you opened it. Reload to see the current version."

const resources = (
  servers: number | null,
  cpu: number | null,
  memory: number | null,
  storage: number | null
) => ({
  servers,
  cpu_millis: cpu,
  memory_bytes: memory,
  storage_bytes: storage,
})

function usage(extra: Record<string, unknown> = {}) {
  return {
    cloud_id: "c",
    accounts: [],
    records: [],
    provisioned: resources(1, 2000, 4 * GIB, 50e9),
    pending: resources(0, 2000, 0, 0),
    reserved: resources(1, 4000, GIB, 0),
    observed: resources(null, null, null, null),
    system_reserve: resources(0, 0, 0, 0),
    allocatable: resources(null, null, null, null),
    headroom: resources(0, 2000, 3 * GIB, 50e9),
    observed_storage_bytes: null,
    checked_at: new Date().toISOString(),
    fresh: true,
    gpus: [],
    frontends: [],
    ...extra,
  }
}

const policy = {
  cloud_id: "c",
  org_id: "o",
  enabled: true,
  require_expansion_approval: true,
  revision: 5,
  limits: {
    servers: 2,
    cpu_millis: 8000,
    memory_bytes: 8 * GIB,
    storage_bytes: 100e9,
  },
  gpu_limits: [{ kind: "nvidia-l4", maximum: 2 }],
  frontend_limits: [],
  pools: [],
}
const view = (p: Record<string, unknown> = policy, automatic = true) => ({
  cloud_id: "c",
  policy: p,
  automatic_expansion: automatic,
})

function expansion(extra: Record<string, unknown> = {}) {
  return {
    id: "x1",
    org_id: "o",
    cloud_id: "c",
    pool_id: "pool_l4",
    connection_id: "do",
    connection_generation: 1,
    account_id: "acct",
    provider: "digitalocean",
    region: "nyc2",
    size: "gpu-l4x1",
    image: "gpu-base",
    architecture: "amd64",
    intent_hash: "h",
    minimum: false,
    operation_id: "",
    policy_revision: 5,
    approved_by: "",
    status: "awaiting_approval",
    revision: 3,
    resources: {
      servers: 1,
      cpu_millis: 8000,
      memory_bytes: 32 * GIB,
      storage_bytes: 200e9,
    },
    template: {
      pool_id: "pool_l4",
      allowed_sizes: [],
      architecture: "amd64",
      bootstrap_size: "",
      connection_id: "do",
      image: "",
    },
    accelerator: {
      count: 1,
      kind: "nvidia-l4",
      memory_gib: 24,
      vendor: "nvidia",
    },
    estimated_hourly_usd: 0.85,
    hourly_price_known: true,
    created_at: "2026-10-27T10:00:00Z",
    updated_at: "2026-10-27T10:00:00Z",
    ...extra,
  }
}

const options = {
  regions: [{ id: "nyc2", name: "New York 2" }],
  images: [
    {
      id: "gpu-base",
      name: "GPU base",
      regions: ["nyc2"],
      bootstrap: true,
      accelerators: ["nvidia"],
    },
    { id: "ubuntu", name: "Ubuntu", regions: ["nyc2"], bootstrap: true },
  ],
  sizes: [
    {
      id: "gpu-l4x1",
      regions: ["nyc2"],
      cpus: 8,
      memory_mib: 32768,
      disk_gb: 200,
      hourly_usd: 0.85,
      monthly_usd: 620,
      accelerator: {
        count: 1,
        kind: "nvidia-l4",
        memory_gib: 24,
        vendor: "nvidia",
      },
    },
    {
      id: "s-2",
      regions: ["nyc2"],
      cpus: 2,
      memory_mib: 4096,
      disk_gb: 80,
      hourly_usd: 0.03,
      monthly_usd: 24,
    },
  ],
}

type Call = { method: string; path: string; body: unknown }
type Reply = { status: number; body: unknown }

function stub({
  usageBody = usage() as unknown,
  views = [view()] as unknown[],
  expansions = [] as unknown[] | ((n: number) => unknown[]),
  approve = [
    { status: 200, body: expansion({ status: "reserved" }) },
  ] as Reply[],
  save = [] as Reply[],
  create = [] as Reply[],
  targets = [] as unknown[],
  servers = [] as unknown[],
} = {}) {
  const calls: Call[] = []
  let approvals = 0
  let saves = 0
  let creates = 0
  let policyReads = 0
  let lists = 0
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      calls.push({
        method,
        path,
        body: init?.body ? JSON.parse(String(init.body)) : null,
      })
      const reply = (r: Reply) => Response.json(r.body, { status: r.status })
      if (path.endsWith("/approve"))
        return reply(approve[Math.min(approvals++, approve.length - 1)]!)
      if (path.endsWith("/scaling-pools"))
        return reply(create[Math.min(creates++, create.length - 1)]!)
      if (path.endsWith("/capacity-policy") && method === "POST")
        return reply(save[Math.min(saves++, save.length - 1)]!)
      if (path.endsWith("/capacity-usage/refresh"))
        return Response.json({ ...(usageBody as object), fresh: true })
      if (path.endsWith("/capacity-usage")) return Response.json(usageBody)
      if (path.endsWith("/capacity-policy"))
        return Response.json(views[Math.min(policyReads++, views.length - 1)])
      if (path.endsWith("/expansions"))
        return Response.json({
          expansions:
            typeof expansions === "function" ? expansions(lists++) : expansions,
        })
      if (path.endsWith("/servers")) return Response.json({ servers })
      if (path.includes("/server-options/")) return Response.json(options)
      if (path.endsWith("/targets")) return Response.json({ targets })
      if (path === "/api/v1/providers")
        return Response.json({
          providers: [
            {
              name: "digitalocean",
              cloud: "digitalocean",
              title: "DigitalOcean",
              available: true,
              infrastructure: ["server:provision"],
              runs: [],
            },
            {
              name: "vercel",
              cloud: "vercel",
              title: "Vercel",
              available: true,
              runs: [{ workload: "web-app", creates: "", execution: "" }],
            },
            {
              name: "rds",
              cloud: "aws",
              title: "RDS",
              available: true,
              runs: [{ workload: "database", creates: "", execution: "" }],
            },
          ],
        })
      return Response.json({})
    }
  )
  return calls
}

const pool = (id: string, name: string, kind?: string): Pool =>
  ({
    id,
    name,
    cloudId: "c",
    orgId: "o",
    revision: 1,
    members: [],
    ...(kind ? { acceleratorKind: kind } : {}),
  }) as Pool
const accounts = [
  {
    id: "do",
    name: "DO US",
    cloud: "digitalocean",
    account: "us",
    status: "ready",
  },
] as Connection[]

function show(pools: Pool[] = [pool("pool_l4", "l4", "nvidia-l4")]) {
  return render(
    <ClientProvider client={forgeClient}>
      <Capacity cloudId="c" pools={pools} connections={accounts} />
    </ClientProvider>
  )
}

const usageRegion = () =>
  screen.findByRole("region", { name: "Capacity usage" })

it("counts pending inside reserved and leads with what the limit counts", async () => {
  stub()
  show()
  const region = await usageRegion()
  await waitFor(() =>
    expect(region).toHaveTextContent(
      "2 used of 2 (1 reserved, 0 starting)Headroom: 0"
    )
  )
  expect(region).toHaveTextContent(
    "6 vCPU used of 8 vCPU (4 vCPU reserved, 2 vCPU starting)"
  )
  expect(region).toHaveTextContent("Storage50 GB used of 100 GB")
})

it("treats a saved ceiling of 0 as a real limit", async () => {
  stub({
    views: [view({ ...policy, limits: { ...policy.limits, servers: 0 } })],
  })
  show()
  const region = await usageRegion()
  await waitFor(() => expect(region).toHaveTextContent("used of 0 ("))
  expect(region).toHaveTextContent("Can't grow: set a ceiling above zero.")
})

it("reads no ceiling only for a policy never saved", async () => {
  stub({ views: [view({ ...policy, revision: 0, enabled: false })] })
  show()
  const region = await usageRegion()
  await waitFor(() => expect(region).toHaveTextContent("used of no ceiling"))
  expect(region).not.toHaveTextContent("Headroom")
})

it("reads unknown usage as unknown, never 0", async () => {
  stub({
    usageBody: usage({
      provisioned: resources(null, null, null, null),
      gpus: [
        {
          kind: "nvidia-l4",
          provisioned: null,
          pending: 0,
          reserved: 1,
          limit: null,
        },
      ],
    }),
  })
  show()
  const region = await usageRegion()
  await waitFor(() =>
    expect(region).toHaveTextContent("unknown used of 2 (1 reserved")
  )
  expect(region).toHaveTextContent("NVIDIA L4: unknown used of no ceiling")
  expect(region).toHaveTextContent(
    "Can't grow: set a GPU ceiling for this kind."
  )
})

it("offers refresh when usage is stale", async () => {
  const user = userEvent.setup()
  const calls = stub({ usageBody: usage({ fresh: false }) })
  show()
  const region = await usageRegion()
  await user.click(
    await within(region).findByRole("button", { name: "Refresh" })
  )
  await waitFor(() =>
    expect(calls.some((c) => c.path.endsWith("/capacity-usage/refresh"))).toBe(
      true
    )
  )
})

const waitingRegion = () =>
  screen.findByRole("region", { name: "Servers waiting for approval" })

it("shows GPU kind, count and monthly cost on an approval", async () => {
  stub({ expansions: [expansion()] })
  show()
  const region = await waitingRegion()
  await waitFor(() => expect(region).toHaveTextContent("1× NVIDIA L4"))
  expect(region).toHaveTextContent("about $621 a month")
  expect(within(region).getByRole("button", { name: "Approve" })).toBeEnabled()
})

it("never prices an unknown expansion at $0", async () => {
  stub({
    expansions: [
      expansion({ estimated_hourly_usd: 0, hourly_price_known: false }),
    ],
  })
  show()
  const region = await waitingRegion()
  await waitFor(() => expect(region).toHaveTextContent("price unknown"))
  expect(region).not.toHaveTextContent("$0")
})

it("offers no Approve on an expansion over a hard limit and says why", async () => {
  stub({
    expansions: [
      expansion({
        block: {
          reason: "hard_limit",
          resource: "gpus:nvidia-l4",
          cloud_id: "c",
          pool_id: "",
          target_id: "",
          limit: 2,
          counted: 2,
          requested: 1,
          policy_revision: 5,
          retryable: false,
        },
      }),
    ],
  })
  show()
  const region = await waitingRegion()
  await waitFor(() =>
    expect(region).toHaveTextContent(
      "NVIDIA L4 GPUs is at this Cloud's limit: 2 of 2 counted, 1 more needed."
    )
  )
  expect(region).toHaveTextContent("To approve it, raise this limit")
  expect(within(region).queryByRole("button", { name: "Approve" })).toBeNull()
})

it("keeps the reload sentence while showing the moved expansion, until the next approval", async () => {
  const user = userEvent.setup()
  const calls = stub({
    expansions: (n) => [expansion(n === 0 ? {} : { revision: 4 })],
    approve: [
      {
        status: 409,
        body: { message: "infrastructure changed; review again" },
      },
      { status: 200, body: expansion({ status: "reserved" }) },
    ],
  })
  show()
  const region = await waitingRegion()
  await user.click(
    await within(region).findByRole("button", { name: "Approve" })
  )
  expect(await within(region).findByText(staleText)).toBeInTheDocument()
  await waitFor(() =>
    expect(calls.filter((c) => c.path.endsWith("/expansions")).length).toBe(2)
  )
  expect(within(region).getByText(staleText)).toBeInTheDocument()
  await user.click(within(region).getByRole("button", { name: "Approve" }))
  await waitFor(() =>
    expect(
      calls.filter((c) => c.path.endsWith("/approve")).at(-1)?.body
    ).toEqual({ expected_revision: 4 })
  )
  await waitFor(() =>
    expect(within(region).queryByText(staleText)).not.toBeInTheDocument()
  )
})

it("asks to try again, not to reload, while the expansion is busy", async () => {
  const user = userEvent.setup()
  stub({
    expansions: [expansion()],
    approve: [
      {
        status: 409,
        body: { message: "infrastructure operation is already running" },
      },
    ],
  })
  show()
  const region = await waitingRegion()
  await user.click(
    await within(region).findByRole("button", { name: "Approve" })
  )
  expect(
    await within(region).findByText(
      "Loomplane is working on this right now. Try again in a moment."
    )
  ).toBeInTheDocument()
})

it("shows the server's sentence when an approval is refused by a limit", async () => {
  const user = userEvent.setup()
  stub({
    expansions: [expansion()],
    approve: [
      {
        status: 422,
        body: {
          message:
            "Servers is at this Cloud's limit: 2 of 2 counted, 1 more needed.",
        },
      },
    ],
  })
  show()
  const region = await waitingRegion()
  await user.click(
    await within(region).findByRole("button", { name: "Approve" })
  )
  expect(
    await within(region).findByText(
      "Servers is at this Cloud's limit: 2 of 2 counted, 1 more needed."
    )
  ).toBeInTheDocument()
})

const growth = () => screen.findByRole("region", { name: "Growth policy" })

it("never turns an empty ceiling into 0", async () => {
  const user = userEvent.setup()
  const calls = stub({
    views: [view({ ...policy, revision: 0, enabled: false, gpu_limits: [] })],
  })
  show([])
  const region = await growth()
  await user.type(
    within(region).getByRole("spinbutton", { name: "Servers" }),
    "3"
  )
  await user.click(within(region).getByRole("button", { name: "Save policy" }))
  expect(await within(region).findByRole("alert")).toHaveTextContent(
    "before you save"
  )
  expect(calls.filter((c) => c.method === "POST")).toEqual([])
})

it("keeps the reload sentence after a stale save, and Reload starts from the newest", async () => {
  const user = userEvent.setup()
  const moved = {
    ...policy,
    revision: 6,
    limits: { ...policy.limits, servers: 5 },
  }
  const calls = stub({
    views: [view(), view(moved)],
    save: [
      {
        status: 409,
        body: { message: "infrastructure changed; review again" },
      },
    ],
  })
  show([])
  const region = await growth()
  const servers = within(region).getByRole("spinbutton", { name: "Servers" })
  await user.clear(servers)
  await user.type(servers, "3")
  await user.click(within(region).getByRole("button", { name: "Save policy" }))
  expect(await within(region).findByText(staleText)).toBeInTheDocument()
  expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
    expected_revision: 5,
  })
  // The newer policy loads underneath, and what was typed stays.
  await waitFor(() =>
    expect(
      calls.filter((c) => c.path.endsWith("/capacity-policy")).length
    ).toBe(3)
  )
  expect(within(region).getByText(staleText)).toBeInTheDocument()
  expect(servers).toHaveValue(3)
  await user.click(within(region).getByRole("button", { name: "Reload" }))
  expect(within(region).queryByText(staleText)).not.toBeInTheDocument()
  expect(
    within(region).getByRole("spinbutton", { name: "Servers" })
  ).toHaveValue(5)
})

it("offers frontend limits only for web app targets and drops gone ones", async () => {
  const user = userEvent.setup()
  const calls = stub({
    views: [
      view({
        ...policy,
        frontend_limits: [
          { target_id: "site", maximum: 3 },
          { target_id: "deleted", maximum: 1 },
        ],
      }),
    ],
    save: [{ status: 200, body: view({ ...policy, revision: 6 }) }],
    targets: [
      {
        id: "site",
        name: "site",
        display_name: "Site",
        provider: "vercel",
        runtime: "managed",
        cloud_id: "c",
        connection_id: "v",
        region: "",
        revision: 1,
      },
      {
        id: "db",
        name: "db",
        display_name: "Db",
        provider: "rds",
        runtime: "managed",
        cloud_id: "c",
        connection_id: "a",
        region: "",
        revision: 1,
      },
    ],
  })
  show([])
  const region = await screen.findByRole("region", { name: "Frontend limits" })
  expect(region).toHaveTextContent("Site")
  expect(region).not.toHaveTextContent("Db")
  const limit = within(region).getByRole("spinbutton", {
    name: "Frontend limit for Site",
  })
  await user.clear(limit)
  await user.type(limit, "4")
  await user.click(
    within(await growth()).getByRole("button", { name: "Save policy" })
  )
  await waitFor(() =>
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      policy: { frontend_limits: [{ target_id: "site", maximum: 4 }] },
    })
  )
})

it("keeps a pool's GPU kind read-only, offers only its sizes and prefills its own limits from the Cloud", async () => {
  const user = userEvent.setup()
  const calls = stub({
    save: [{ status: 200, body: view({ ...policy, revision: 6 }) }],
  })
  show()
  await user.click(
    await screen.findByRole("button", { name: "Add the rule for pool l4" })
  )
  const dialog = await screen.findByRole("dialog")
  expect(dialog).toHaveTextContent("GPU kind: NVIDIA L4")
  await user.click(within(dialog).getByRole("combobox", { name: "Account" }))
  await user.click(await screen.findByRole("option", { name: "DO US" }))
  await user.click(
    await within(dialog).findByRole("combobox", { name: "Region" })
  )
  await user.click(await screen.findByRole("option", { name: "New York 2" }))
  await user.click(within(dialog).getByRole("combobox", { name: "Image" }))
  expect(screen.queryByRole("option", { name: "Ubuntu" })).toBeNull()
  await user.click(await screen.findByRole("option", { name: "GPU base" }))
  expect(within(dialog).queryByText("s-2")).toBeNull()
  await user.click(within(dialog).getByRole("checkbox", { name: /gpu-l4x1/ }))
  await user.click(
    within(dialog).getByRole("checkbox", {
      name: "Tighter limits for this pool",
    })
  )
  expect(
    within(dialog).getByRole("spinbutton", { name: "Pool Servers" })
  ).toHaveValue(2)
  expect(
    within(dialog).getByRole("spinbutton", { name: "Pool NVIDIA L4 GPUs" })
  ).toHaveValue(2)
  await user.click(within(dialog).getByRole("button", { name: "Done" }))
  await user.click(
    within(await growth()).getByRole("button", { name: "Save policy" })
  )
  await waitFor(() =>
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      expected_revision: 5,
      policy: {
        pools: [
          {
            pool_id: "pool_l4",
            accelerator_kind: "nvidia-l4",
            image: "gpu-base",
            allowed_sizes: ["gpu-l4x1"],
            limits: {
              servers: 2,
              cpu_millis: 8000,
              memory_bytes: 8 * GIB,
              storage_bytes: 100e9,
              gpu_kind: "nvidia-l4",
              gpus: 2,
            },
          },
        ],
      },
    })
  )
})

it("creates an empty GPU pool and its rule in one request", async () => {
  const user = userEvent.setup()
  const onPools = vi.fn()
  const calls = stub({
    create: [
      { status: 200, body: { pool: {}, policy: { ...policy, revision: 6 } } },
    ],
  })
  render(
    <ClientProvider client={forgeClient}>
      <Capacity
        cloudId="c"
        pools={[]}
        connections={accounts}
        onPoolsChanged={onPools}
      />
    </ClientProvider>
  )
  await user.click(
    await screen.findByRole("button", { name: "New scaling pool" })
  )
  const dialog = await screen.findByRole("dialog")
  await user.type(
    within(dialog).getByRole("textbox", { name: "Pool name" }),
    "batch"
  )
  await user.click(within(dialog).getByRole("combobox", { name: "GPU kind" }))
  await user.click(await screen.findByRole("option", { name: "NVIDIA L4" }))
  await user.click(within(dialog).getByRole("combobox", { name: "Account" }))
  await user.click(await screen.findByRole("option", { name: "DO US" }))
  await user.click(
    await within(dialog).findByRole("combobox", { name: "Region" })
  )
  await user.click(await screen.findByRole("option", { name: "New York 2" }))
  await user.click(within(dialog).getByRole("combobox", { name: "Image" }))
  await user.click(await screen.findByRole("option", { name: "GPU base" }))
  await user.click(within(dialog).getByRole("checkbox", { name: /gpu-l4x1/ }))
  await user.click(within(dialog).getByRole("button", { name: "Create pool" }))
  await waitFor(() => expect(onPools).toHaveBeenCalled())
  expect(
    calls.find((c) => c.path.endsWith("/scaling-pools"))?.body
  ).toMatchObject({
    name: "batch",
    accelerator_kind: "nvidia-l4",
    expected_revision: 5,
    policy: { enabled: true, revision: 5 },
    template: {
      accelerator_kind: "nvidia-l4",
      connection_id: "do",
      image: "gpu-base",
      allowed_sizes: ["gpu-l4x1"],
    },
  })
})

it("asks for a GPU ceiling of the new pool's kind, apart from the Cloud limits", async () => {
  const user = userEvent.setup()
  stub()
  show([])
  await user.click(
    await screen.findByRole("button", { name: "New scaling pool" })
  )
  const dialog = await screen.findByRole("dialog")
  await user.click(within(dialog).getByRole("combobox", { name: "GPU kind" }))
  await user.click(
    await screen.findByRole("option", { name: "NVIDIA H100 80GB" })
  )
  expect(dialog).toHaveTextContent(
    "A GPU pool needs a GPU ceiling for NVIDIA H100 80GB. Add one under Growth policy and save it first."
  )
  expect(dialog).not.toHaveTextContent("needs saved limits above zero")
  expect(
    within(dialog).getByRole("button", { name: "Create pool" })
  ).toBeDisabled()
})

it("says the new pool stays empty while automatic expansion is off", async () => {
  const user = userEvent.setup()
  stub({ views: [view(policy, false)] })
  show([])
  await user.click(
    await screen.findByRole("button", { name: "New scaling pool" })
  )
  expect(await screen.findByRole("dialog")).toHaveTextContent(
    "this pool stays empty until you add a server to it"
  )
})
