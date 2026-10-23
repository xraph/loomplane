import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
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
      if (path.endsWith("/dns-zones")) return Response.json({ zones: [] })
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
  await user.selectOptions(
    screen.getByRole("combobox", { name: "Server account" }),
    "ssh"
  )
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
  await user.selectOptions(
    screen.getByRole("combobox", { name: "Server account" }),
    "ssh"
  )
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
      if (path.endsWith("/dns-zones")) return Response.json({ zones: [] })
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
  await user.selectOptions(
    screen.getByRole("combobox", { name: "DNS mode" }),
    "managed"
  )
  expect(
    screen.queryByRole("option", { name: "Compute only" })
  ).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Save DNS zone" })).toBeDisabled()
  await user.click(await screen.findByRole("checkbox", { name: "Web app" }))
  await user.selectOptions(
    screen.getByRole("combobox", { name: "DNS account" }),
    "dns"
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
  expect(await screen.findByText("1 servers")).toBeInTheDocument()
  await user.click(
    await screen.findByRole("button", { name: "Retry operation" })
  )
  expect(await screen.findByText("2 servers")).toBeInTheDocument()
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
  await user.selectOptions(
    screen.getByRole("combobox", { name: "Placement runtime" }),
    "server"
  )
  await user.selectOptions(
    await screen.findByRole("combobox", { name: "Target server pool" }),
    "pool"
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
  expect(
    await screen.findByText(/resize shared-host: waiting/)
  ).toBeInTheDocument()
  expect(await screen.findByText(/provider \/ new-account/)).toBeInTheDocument()
})
