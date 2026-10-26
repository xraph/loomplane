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
import { Servers } from "./servers"
import { Domains } from "./domains"

let denied = false
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => !denied }),
}))
afterEach(() => {
  denied = false
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
const cloud = { id: "c", revision: 4, name: "Shared" }
const writes: { path: string; body: Record<string, unknown> }[] = []
function fixture(stale = false) {
  writes.length = 0
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const body = JSON.parse(String(init?.body ?? "{}"))
      if (init?.method && init.method !== "GET") {
        writes.push({ path, body })
        if (path.endsWith("/servers/preview"))
          return Response.json({
            id: "op",
            review: "immutable",
            status: "preview",
            input: body,
            actions: [],
            connection_generation: 7,
          })
        if (path.endsWith("/apply") && stale)
          return Response.json(
            { message: "infrastructure changed; review again" },
            { status: 409 }
          )
        if (path.endsWith("/apply"))
          return Response.json({
            id: "op",
            review: "immutable",
            status: "waiting",
            input: {},
            actions: [],
            message: "Waiting for verified enrollment",
          })
        return Response.json({
          id: "r",
          revision: 8,
          input: { cloud_id: "c", hostname: "app.example.test" },
          verification: {},
        })
      }
      if (path.endsWith("/connections"))
        return Response.json({
          connections: [
            { id: "ssh", name: "Pinned SSH", cloud: "server", status: "ready" },
          ],
        })
      if (path.endsWith("/providers")) return Response.json({ providers: [] })
      if (path.endsWith("/servers")) return Response.json({ servers: [] })
      if (path.endsWith("/pools")) return Response.json({ pools: [] })
      if (path.endsWith("/infrastructure-operations"))
        return Response.json({ operations: [] })
      if (path.endsWith("/dns-zones"))
        return Response.json({
          zones: path.includes("/projects/")
            ? [
                {
                  id: "external",
                  name: "example.test",
                  mode: "external",
                  verified: true,
                },
              ]
            : [],
        })
      if (path.endsWith("/domain-routes"))
        return Response.json({
          routes: [
            {
              id: "r",
              project_id: "p",
              revision: 7,
              status: "ready",
              input: {
                cloud_id: "c",
                hostname: "app.example.test",
                zone_id: "external",
                service: "api",
                environment_id: "env",
                external_ttl_seconds: 300,
              },
              binding: {},
              verification: { dns: true, tls: false, backend: true },
            },
          ],
        })
      return Response.json({})
    }
  )
}
function show(element: React.ReactNode) {
  render(<ClientProvider client={forgeClient}>{element}</ClientProvider>)
}
it("reviews generic SSH adoption even with no provider provisioning capability, then applies the exact review", async () => {
  fixture()
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  await user.click(
    await screen.findByRole("button", { name: "Connect existing server" })
  )
  await user.type(
    screen.getByRole("textbox", { name: "Server name" }),
    "shared-host"
  )
  await user.click(screen.getByRole("combobox", { name: "Server account" }))
  await user.click(await screen.findByRole("option", { name: /Pinned SSH/ }))
  await user.click(screen.getByRole("checkbox", { name: /Confirm pinned SSH/ }))
  await user.click(screen.getByRole("button", { name: "Review server" }))
  expect(
    await screen.findByText(/Connection generation: 7/)
  ).toBeInTheDocument()
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  expect(
    await screen.findByText("Waiting for verified enrollment")
  ).toBeInTheDocument()
  expect(writes[0]!.body).toMatchObject({
    kind: "adopt",
    cloud_revision: 4,
    connection_id: "ssh",
    confirm_adoption: true,
  })
  expect(writes[1]).toEqual({
    path: "/api/v1/clouds/c/infrastructure-operations/op/apply",
    body: { review: "immutable" },
  })
})
it("invalidates a stale server review and requires another preview", async () => {
  fixture(true)
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  await user.click(
    await screen.findByRole("button", { name: "Connect existing server" })
  )
  await user.type(screen.getByRole("textbox", { name: "Server name" }), "host")
  await user.click(screen.getByRole("combobox", { name: "Server account" }))
  await user.click(await screen.findByRole("option", { name: /Pinned SSH/ }))
  await user.click(screen.getByRole("checkbox", { name: /Confirm pinned SSH/ }))
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    await screen.findByRole("button", { name: "Apply reviewed server" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(/review again/)
  expect(
    screen.queryByRole("button", { name: "Apply reviewed server" })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Review server" })
  ).toBeInTheDocument()
})
it("keeps route DNS, TLS and backend states separate and CAS-updates the previously served external TTL", async () => {
  fixture()
  const user = userEvent.setup()
  show(<Domains cloud={cloud as never} projectId="p" />)
  expect(await screen.findByText("app.example.test")).toBeInTheDocument()
  expect(screen.getByText("TLS pending")).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Update external TTL" }))
  const input = screen.getByRole("spinbutton", {
    name: "Maximum previously served TTL (seconds)",
  })
  await user.clear(input)
  await user.type(input, "900")
  await user.click(screen.getByRole("button", { name: "Save TTL bound" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      path: "/api/v1/projects/p/domain-routes/r/ttl",
      body: { revision: 7, external_ttl_seconds: 900 },
    })
  )
})

it("renders exact retained restore separately from managed recreation and public route drain progress", async () => {
  const { EnvironmentProgress } = await import("./organization-activity")
  render(
    <EnvironmentProgress
      environment={
        {
          environmentId: "env",
          environment: "production",
          status: "routing_pending",
          services: ["api"],
          compensationMode: "restore_retained",
          compensationMessage: "Exact retained identity",
          routingStatus: "draining",
          routes: [
            {
              routeId: "r",
              hostname: "api.example.test",
              recordType: "A",
              value: "192.0.2.20",
              ttl: 300,
              status: "waiting",
              message: "Wait for previous TTL",
              notBefore: "2027-01-01T00:00:00Z",
            },
          ],
        } as never
      }
    />
  )
  expect(
    screen.getByText("Recovery: Restore exact retained runtime")
  ).toBeInTheDocument()
  expect(
    screen.getByText(/Publish A api.example.test = 192.0.2.20/)
  ).toBeInTheDocument()
  expect(screen.getByText(/Previous route retained until/)).toBeInTheDocument()
  expect(
    screen.queryByText(/Recreate from saved inputs/)
  ).not.toBeInTheDocument()
})

it("uses an independent DNS account and an explicit project grant when saving a managed zone", async () => {
  const sent: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body))
        sent.push({ path, body })
        return Response.json({
          ...body,
          id: "zone",
          proof_name: "_loomplane.example.test",
          proof_value: "proof",
          project_ids: ["p"],
        })
      }
      if (path.endsWith("/providers"))
        return Response.json({
          providers: [
            {
              name: "dns-provider",
              cloud: "dns-provider",
              available: true,
              infrastructure: ["dns:records:upsert"],
            },
          ],
        })
      if (path.endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: "compute",
              name: "Compute only",
              cloud: "compute",
              status: "ready",
            },
            {
              id: "dns",
              name: "Independent DNS",
              cloud: "dns-provider",
              status: "ready",
            },
          ],
        })
      if (path.endsWith("/projects"))
        return Response.json({ projects: [{ id: "p", name: "Web app" }] })
      if (path.endsWith("/dns-zones"))
        return Response.json({
          zones: path.includes("/projects/")
            ? [
                {
                  id: "external",
                  name: "example.test",
                  mode: "external",
                  verified: true,
                },
              ]
            : [],
        })
      return Response.json({ routes: [] })
    }
  )
  const user = userEvent.setup()
  show(<Domains cloud={cloud as never} projectId="p" />)
  await user.click(await screen.findByRole("button", { name: "Add DNS zone" }))
  await user.type(
    screen.getByRole("textbox", { name: "Zone name" }),
    "example.test"
  )
  await user.click(screen.getByRole("combobox", { name: "DNS mode" }))
  await user.click(await screen.findByRole("option", { name: /Managed DNS/ }))
  expect(screen.getByRole("button", { name: "Save DNS zone" })).toBeDisabled()
  await user.click(await screen.findByRole("checkbox", { name: "Web app" }))
  await user.click(screen.getByRole("combobox", { name: "DNS account" }))
  expect(
    await screen.findByRole("option", { name: /Independent DNS/ })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("option", { name: "Compute only" })
  ).not.toBeInTheDocument()
  await user.click(
    await screen.findByRole("option", { name: /Independent DNS/ })
  )
  await user.click(screen.getByRole("button", { name: "Save DNS zone" }))
  await waitFor(() =>
    expect(sent).toContainEqual({
      path: "/api/v1/dns-zones",
      body: {
        name: "example.test",
        mode: "managed",
        connection_id: "dns",
        project_ids: ["p"],
      },
    })
  )
})

it("does not request organization server inventory when the grant is denied", () => {
  denied = true
  const fetch = vi.fn()
  vi.stubGlobal("fetch", fetch)
  show(<Servers cloud={cloud as never} />)
  expect(screen.getByText("Server inventory is restricted")).toBeInTheDocument()
  expect(fetch).not.toHaveBeenCalled()
})

it("refreshes cached shared inventory after an operation retry completes enrollment", async () => {
  const { InfrastructureActivity } = await import("./servers")
  let enrolled = false
  const host = (id: string) => ({
    id,
    name: id,
    ownership: "adopted",
    status: "ready",
    health: {
      effective_cpu_millis: 2000,
      effective_memory_bytes: 4294967296,
      checked_at: "2026-10-22T12:00:00Z",
    },
  })
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const operation = {
        id: "op",
        input: { kind: "adopt", name: "new-host" },
        status: enrolled ? "succeeded" : "waiting",
        attempt: 1,
        actions: [],
      }
      if (path.endsWith("/retry") && init?.method === "POST") {
        enrolled = true
        return Response.json({ ...operation, status: "succeeded" })
      }
      if (path.endsWith("/infrastructure-operations"))
        return Response.json({ operations: [operation] })
      if (path.endsWith("/servers"))
        return Response.json({
          servers: enrolled
            ? [host("original"), host("new-host")]
            : [host("original")],
        })
      if (path.endsWith("/usage"))
        return Response.json({
          workloads: [],
          retained_storage: [],
          reserved_cpu_millis: 0,
          reserved_memory_bytes: 0,
        })
      if (path.endsWith("/connections"))
        return Response.json({ connections: [] })
      if (path.endsWith("/providers")) return Response.json({ providers: [] })
      return Response.json({ pools: [] })
    }
  )
  const user = userEvent.setup()
  show(
    <>
      <Servers cloud={cloud as never} />
      <InfrastructureActivity cloudId="c" />
    </>
  )
  expect(
    await screen.findByRole("heading", { name: "Servers 1" })
  ).toBeInTheDocument()
  await user.click(
    await screen.findByRole("button", { name: "Retry operation" })
  )
  expect(
    await screen.findByRole("heading", { name: "Servers 2" })
  ).toBeInTheDocument()
})

it("releases a handoff through its exact preparation with a bounded reviewed drain interval", async () => {
  const sent: { path: string; body: Record<string, unknown> }[] = []
  const route = {
    id: "r",
    project_id: "p",
    revision: 7,
    status: "ready",
    input: {
      cloud_id: "c",
      hostname: "api.example.test",
      environment_id: "env",
      service: "api",
      external_ttl_seconds: 300,
    },
    binding: {},
    verification: { dns: true, tls: true, backend: true },
  }
  const cutover = {
    id: "cutover",
    handoff_id: "preparation",
    kind: "cutover",
    status: "succeeded",
    route,
    attempt: 1,
    not_before: "0001-01-01T00:00:00Z",
    desired_record: {},
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        sent.push({ path, body: JSON.parse(String(init.body)) })
        return Response.json({
          ...cutover,
          id: "release",
          kind: "release",
          status: "pending",
        })
      }
      if (path.endsWith("/domain-routes"))
        return Response.json({ routes: [route] })
      if (path.endsWith("/operations"))
        return Response.json({ operations: [cutover] })
      if (path.endsWith("/projects")) return Response.json({ projects: [] })
      return Response.json({ zones: [] })
    }
  )
  const user = userEvent.setup()
  show(<Domains cloud={cloud as never} projectId="p" />)
  await user.click(await screen.findByRole("button", { name: "Inspect route" }))
  const interval = screen.getByRole("spinbutton", {
    name: "Handoff drain interval (seconds)",
  })
  await user.clear(interval)
  await user.type(interval, "600")
  await user.click(
    screen.getByRole("button", { name: "Release retained route" })
  )
  await waitFor(() =>
    expect(sent).toContainEqual({
      path: "/api/v1/projects/p/route-operations/preparation/release-handoff",
      body: { drain_seconds: 600 },
    })
  )
})

it("creates a pool target without a managed provider account and preserves target tags", async () => {
  const { TargetEditor } = await import("./target-editor")
  const writes: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body))
        writes.push(body)
        return Response.json({ ...body, id: "target" })
      }
      if (path.endsWith("/pools"))
        return Response.json({
          pools: [
            {
              id: "pool",
              name: "Shared servers",
              members: [{ server_id: "host" }],
            },
          ],
        })
      if (path.endsWith("/connections"))
        return Response.json({ connections: [] })
      return Response.json({ providers: [] })
    }
  )
  const user = userEvent.setup()
  show(
    <TargetEditor
      projectId=""
      profile={cloud as never}
      onClose={() => undefined}
      onSaved={() => undefined}
    />
  )
  await user.type(
    screen.getByRole("textbox", { name: "Target slug" }),
    "backend"
  )
  await user.click(screen.getByRole("combobox", { name: "Placement runtime" }))
  await user.click(await screen.findByRole("option", { name: /Server pool/ }))
  await user.click(
    await screen.findByRole("combobox", { name: "Target server pool" })
  )
  await user.click(
    await screen.findByRole("option", { name: /Shared servers/ })
  )
  await user.click(screen.getByRole("button", { name: "Save target" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      name: "backend",
      display_name: "backend",
      provider: "server",
      connection_id: "",
      region: "",
      runtime: "server",
      pool_id: "pool",
      tags: [],
    })
  )
})

it("refreshes both persisted activity lists when returning after server or placement changes", async () => {
  const { InfrastructureActivity } = await import("./servers")
  const { OrganizationActivity } = await import("./organization-activity")
  let changed = false
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("/infrastructure-operations"))
      return Response.json({
        operations: changed
          ? [
              {
                id: "op",
                input: { kind: "resize", name: "shared-host" },
                status: "waiting",
                attempt: 1,
                actions: [],
              },
            ]
          : [],
      })
    return Response.json({
      syncs: changed
        ? [
            {
              id: "sync",
              cloud_id: "c",
              status: "pending",
              attempt: 1,
              environments: [],
              default: true,
              after_default: {
                provider: "provider",
                connection_id: "new-account",
              },
            },
          ]
        : [],
    })
  })
  const content = (visible: boolean) => (
    <ClientProvider client={forgeClient}>
      {visible && (
        <>
          <InfrastructureActivity cloudId="c" />
          <OrganizationActivity cloudId="c" />
        </>
      )}
    </ClientProvider>
  )
  const { rerender } = render(content(true))
  expect(
    await screen.findByText("No infrastructure operations")
  ).toBeInTheDocument()
  expect(await screen.findByText("No Cloud syncs")).toBeInTheDocument()
  rerender(content(false))
  changed = true
  rerender(content(true))
  expect(await screen.findByText("resize shared-host")).toBeInTheDocument()
  expect(screen.getByText("Waiting")).toBeInTheDocument()
  expect(await screen.findByText(/provider \/ new-account/)).toBeInTheDocument()
})

const l4Device = (index: number) => ({
  index,
  uuid: `GPU-${index}`,
  kind: "nvidia-l4",
  product: "NVIDIA L4",
  memory_mib: 23034,
})
const gpuHealth = {
  effective_cpu_millis: 8000,
  effective_memory_bytes: 34359738368,
  checked_at: "2026-10-25T12:00:00Z",
}
function gpuFixture({
  servers = [],
  pools = [],
  operation = {},
}: {
  servers?: Record<string, unknown>[]
  pools?: Record<string, unknown>[]
  operation?: Record<string, unknown>
} = {}) {
  writes.length = 0
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const body = JSON.parse(String(init?.body ?? "{}"))
      if (init?.method && init.method !== "GET") {
        writes.push({ path, body })
        if (path.endsWith("/servers/preview"))
          return Response.json({
            id: "op",
            review: "immutable",
            status: "preview",
            input: body,
            actions: [],
            ...operation,
          })
        return Response.json({ id: "pool", revision: 1, members: [] })
      }
      if (path.endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: "do",
              name: "DigitalOcean",
              cloud: "digitalocean",
              status: "ready",
            },
          ],
        })
      if (path.endsWith("/providers"))
        return Response.json({
          providers: [
            {
              name: "digitalocean",
              cloud: "digitalocean",
              available: true,
              infrastructure: ["server:provision", "server:resize"],
            },
          ],
        })
      if (path.includes("/server-options/"))
        return Response.json({
          regions: [{ id: "tor1", name: "Toronto 1" }],
          sizes: [
            {
              id: "s-2vcpu-4gb",
              cpus: 2,
              memory_mib: 4096,
              disk_gb: 80,
              hourly_usd: 0.03571,
              hourly_price_known: true,
              monthly_usd: 24,
              regions: ["tor1"],
            },
            {
              id: "gpu-h100x1-80gb",
              cpus: 20,
              memory_mib: 245760,
              disk_gb: 720,
              hourly_usd: 3.39,
              hourly_price_known: true,
              monthly_usd: 2474.7,
              regions: ["tor1"],
              accelerator: {
                count: 1,
                kind: "nvidia-h100-80gb",
                memory_gib: 80,
                vendor: "nvidia",
              },
            },
          ],
          images: [
            {
              id: "ubuntu-24-04-x64",
              name: "ubuntu-24-04-x64",
              bootstrap: true,
              regions: ["tor1"],
            },
            {
              id: "gpu-h100x1-base",
              name: "gpu-h100x1-base",
              bootstrap: true,
              accelerators: ["nvidia"],
              regions: ["tor1"],
            },
          ],
        })
      if (path.endsWith("/servers")) return Response.json({ servers })
      if (path.endsWith("/usage"))
        return Response.json({
          workloads: [],
          retained_storage: [],
          reserved_cpu_millis: 0,
          reserved_memory_bytes: 0,
          reserved_gpus: 0,
        })
      if (path.endsWith("/pools")) return Response.json({ pools })
      return Response.json({})
    }
  )
}
async function openCreate(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    (await screen.findAllByRole("button", { name: "Create server" }))[0]!
  )
  await user.type(screen.getByRole("textbox", { name: "Server name" }), "gpu")
  await user.click(screen.getByRole("combobox", { name: "Server account" }))
  await user.click(await screen.findByRole("option", { name: "DigitalOcean" }))
}
const h100Option =
  "gpu-h100x1-80gb · 1× NVIDIA H100 80GB · 80 GiB · 20 CPU · $3.39/hr"

it("groups GPU sizes and prices them per hour", async () => {
  gpuFixture()
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  await openCreate(user)
  await user.click(await screen.findByRole("combobox", { name: "Server size" }))
  const gpu = await screen.findByRole("group", { name: "GPU" })
  expect(
    within(gpu).getByRole("option", { name: h100Option })
  ).toBeInTheDocument()
  // CPU sizes keep their line and stay out of the GPU group.
  expect(
    screen.getByRole("option", { name: /^s-2vcpu-4gb · 2 CPU · 4096 MiB/ })
  ).toBeInTheDocument()
  expect(
    within(gpu).queryByRole("option", { name: /s-2vcpu-4gb/ })
  ).not.toBeInTheDocument()
})

it("only offers GPU-ready images for a GPU size", async () => {
  gpuFixture()
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  await openCreate(user)
  await user.click(await screen.findByRole("combobox", { name: "Server size" }))
  await user.click(await screen.findByRole("option", { name: h100Option }))
  const image = screen.getByRole("combobox", { name: "Server image" })
  expect(image).toHaveTextContent("gpu-h100x1-base")
  await user.click(image)
  expect(
    await screen.findByRole("option", { name: "gpu-h100x1-base" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("option", { name: "ubuntu-24-04-x64" })
  ).not.toBeInTheDocument()
})

it("shows the monthly estimate before creating a GPU server", async () => {
  gpuFixture({
    operation: {
      estimated_hourly_usd: 3.39,
      hourly_price_known: true,
      accelerator: {
        count: 1,
        kind: "nvidia-h100-80gb",
        memory_gib: 80,
        vendor: "nvidia",
      },
    },
  })
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  await openCreate(user)
  await user.click(screen.getByRole("button", { name: "Review server" }))
  expect(
    await screen.findByText(
      "Estimated cost: $3.39/hr, about $2,475/mo while it runs"
    )
  ).toBeInTheDocument()
  expect(screen.queryByText(/Estimated hourly cost/)).not.toBeInTheDocument()
})

it("never shows an unknown GPU price as $0", async () => {
  gpuFixture({
    servers: [
      {
        id: "s1",
        name: "gpu-unpriced",
        status: "ready",
        ownership: "managed",
        size: "gpu-h100x1-80gb",
        accelerator: {
          count: 1,
          kind: "nvidia-h100-80gb",
          memory_gib: 80,
          vendor: "nvidia",
        },
        hourly_usd: 0,
        health: { ...gpuHealth, gpu_runtime: "ready" },
      },
    ],
    operation: {
      estimated_hourly_usd: 0,
      hourly_price_known: false,
      accelerator: {
        count: 1,
        kind: "nvidia-h100-80gb",
        memory_gib: 80,
        vendor: "nvidia",
      },
    },
  })
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  expect(
    await screen.findByText(/1× NVIDIA H100 80GB · price unknown/)
  ).toBeInTheDocument()
  await openCreate(user)
  await user.click(screen.getByRole("button", { name: "Review server" }))
  expect(
    await screen.findByText("Estimated cost: price unknown")
  ).toBeInTheDocument()
  expect(screen.queryByText(/\$0(\.00)?\b/)).not.toBeInTheDocument()
})

it("marks a GPU server that can't take GPU work", async () => {
  const message =
    "The NVIDIA Container Toolkit isn't installed, so Docker can't give containers GPUs."
  gpuFixture({
    servers: [
      {
        id: "s1",
        name: "gpu-host",
        status: "ready",
        ownership: "managed",
        size: "gpu-h100x1-80gb",
        priced_size: "gpu-h100x1-80gb",
        accelerator: {
          count: 1,
          kind: "nvidia-h100-80gb",
          memory_gib: 80,
          vendor: "nvidia",
        },
        hourly_usd: 3.39,
        hourly_price_known: true,
        health: {
          ...gpuHealth,
          gpu_runtime: "toolkit_missing",
          gpu_message: message,
        },
      },
    ],
  })
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  expect(
    await screen.findByText(/1× NVIDIA H100 80GB · \$3\.39\/hr/)
  ).toBeInTheDocument()
  const badge = screen.getByText("Can't take GPU work")
  await user.hover(badge)
  expect(await screen.findByText(message)).toBeInTheDocument()
  await user.unhover(badge)
  await user.click(screen.getByRole("button", { name: "Inspect gpu-host" }))
  const details = await screen.findByRole("dialog")
  expect(within(details).getByText(message)).toBeInTheDocument()
  expect(
    within(details).getByText(/about \$2,475\/mo while it runs/)
  ).toBeInTheDocument()
})

it("budgets GPUs per member in a GPU pool", async () => {
  gpuFixture({
    servers: [
      {
        id: "gpu",
        name: "gpu-box",
        status: "ready",
        ownership: "adopted",
        health: {
          ...gpuHealth,
          gpu_runtime: "ready",
          gpus: [l4Device(0), l4Device(1)],
        },
      },
      {
        id: "cpu",
        name: "cpu-box",
        status: "ready",
        ownership: "adopted",
        health: { ...gpuHealth, gpu_runtime: "none" },
      },
    ],
  })
  const user = userEvent.setup()
  show(<Servers cloud={cloud as never} />)
  await user.click(await screen.findByRole("button", { name: "Add pool" }))
  await user.type(screen.getByRole("textbox", { name: "Pool name" }), "gpus")
  await user.click(screen.getByRole("checkbox", { name: "gpu-box" }))
  await user.click(screen.getByRole("combobox", { name: "GPU kind" }))
  await user.click(await screen.findByRole("option", { name: "NVIDIA L4" }))
  const budget = screen.getByRole("spinbutton", {
    name: "GPU budget for gpu-box",
  })
  expect(budget).toHaveAttribute("max", "2")
  expect(budget).toHaveValue(2)
  expect(screen.getByRole("checkbox", { name: "cpu-box" })).toHaveAttribute(
    "aria-disabled",
    "true"
  )
  expect(screen.getByText("No NVIDIA L4 GPUs")).toBeInTheDocument()
  await user.clear(budget)
  await user.type(budget, "1")
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]!.body).toMatchObject({
    name: "gpus",
    accelerator_kind: "nvidia-l4",
    members: [{ server_id: "gpu", gpus: 1 }],
  })
})

it("says why a GPU pool can't grow while a server's GPUs are unread", async () => {
  gpuFixture({
    servers: [
      {
        id: "gpu",
        name: "gpu-box",
        status: "ready",
        ownership: "adopted",
        health: { ...gpuHealth, gpu_runtime: "unknown" },
      },
    ],
    pools: [
      {
        id: "p",
        name: "inference",
        revision: 2,
        accelerator_kind: "nvidia-l4",
        members: [
          {
            server_id: "gpu",
            cpu_millis: 8000,
            memory_bytes: 34359738368,
            draining: false,
            gpus: 2,
          },
        ],
      },
    ],
  })
  show(<Servers cloud={cloud as never} />)
  expect(
    await screen.findByText(
      "The GPUs on gpu-box couldn't be read, so Loomplane won't add GPU servers to this Cloud until a health check there succeeds."
    )
  ).toBeInTheDocument()
  expect(screen.getByText(/2 NVIDIA L4 GPUs/)).toBeInTheDocument()
})
