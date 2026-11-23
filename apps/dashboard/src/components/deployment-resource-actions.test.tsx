import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import type { Manifest } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { DeploymentResourceActions } from "@/components/deployment-resource-actions"

let canPermission: (perm: string) => boolean = () => true
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (perm: string) => canPermission(perm), session: {} }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

afterEach(() => {
  cleanup()
  canPermission = () => true
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const doProvider = {
  name: "digitalocean",
  title: "DigitalOcean",
  cloud: "digitalocean",
  available: true,
  regions: ["nyc3", "sfo3"],
  provisions: [
    {
      kind: "postgres",
      creates: "DigitalOcean Managed Postgres",
      versions: ["16", "17"],
      settings: [
        { name: "size", title: "Size", required: true, capacity: true, default: "db-s-1vcpu-1gb", pattern: "" },
        { name: "nodes", title: "Nodes", required: true, capacity: true, default: "1", pattern: "" },
      ],
    },
  ],
}

const serverProvider = {
  name: "server",
  title: "Server",
  cloud: "server",
  available: true,
  provisions: [{ kind: "postgres", creates: "Postgres container", versions: ["17"] }],
}

const defaultConnections = [
  { id: "conn_do_1", name: "DigitalOcean", account: "acct-1", cloud: "digitalocean", status: "ready", details: { region: "nyc3" } },
  { id: "conn_do_2", name: "Digital Ocean Wakflo", account: "acct-2", cloud: "digitalocean", status: "ready", details: { region: "nyc3" } },
  { id: "conn_srv_1", name: "Host A", account: "host-a", cloud: "server", status: "ready", details: {} },
]

function setupFetch({
  manifest,
  revision = 11,
  providerResources = [],
  saveStatus = 200,
  onSave,
  onEdit,
  onProviderResources,
  onManifestGet,
}: {
  manifest: Manifest
  revision?: number
  providerResources?: unknown[]
  saveStatus?: number
  onSave?: (body: unknown) => void
  onEdit?: (body: unknown) => void
  onProviderResources?: () => void
  onManifestGet?: () => void
}) {
  let rev = revision
  let currentManifest = manifest
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    if (url.endsWith("/manifest") && method === "PUT") {
      const body = JSON.parse(String(init?.body)) as { manifest?: Manifest }
      onSave?.(body)
      if (saveStatus >= 400) {
        return json({ message: "Someone saved the manifest at the same time." }, saveStatus)
      }
      rev++
      if (body.manifest) currentManifest = body.manifest
      return json({ id: `rev_${rev}`, revision: rev, manifest: currentManifest, yaml: "version: 1\n" })
    }
    if (url.endsWith("/manifest/edits") && method === "POST") {
      onEdit?.(JSON.parse(String(init?.body)))
      rev++
      return json({ id: `rev_${rev}`, revision: rev, manifest: currentManifest, yaml: "version: 1\n" })
    }
    if (url.endsWith("/manifest")) {
      onManifestGet?.()
      return json({ id: `rev_${rev}`, revision: rev, manifest: currentManifest, yaml: "version: 1\n" })
    }
    if (url.endsWith("/environments")) {
      return json({
        environments: [
          { id: "env_stg", name: "staging" },
          { id: "env_prd", name: "production" },
        ],
      })
    }
    if (url.endsWith("/providers")) return json({ providers: [doProvider, serverProvider] })
    if (url.endsWith("/production/connections")) return json({ environment: "production", connections: { digitalocean: "conn_do_1" } })
    if (url.endsWith("/connections")) return json({ connections: defaultConnections })
    if (url.endsWith("/default-cloud")) return json({})
    if (url.endsWith("/clouds")) return json({ clouds: [] })
    if (url.includes("/bindings")) return json({ bindings: [] })
    if (url.includes("/digitalocean-database-capacity")) {
      return json({
        pg: {
          regions: ["nyc3"],
          layouts: [{ num_nodes: 1, sizes: ["db-s-1vcpu-1gb", "db-s-2vcpu-4gb"] }],
        },
        mysql: { regions: [], layouts: [] },
        valkey: { regions: [], layouts: [] },
      })
    }
    if (url.endsWith("/provider-resources")) {
      onProviderResources?.()
      return json({ resources: providerResources })
    }
    return json({})
  })
}

function renderActions(name = "postgres", replan = vi.fn().mockResolvedValue(undefined)) {
  const onSaved = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentResourceActions
        projectId="proj_1"
        environment="production"
        name={name}
        onSaved={onSaved}
        replan={replan}
      />
    </ClientProvider>
  )
  return { onSaved, replan }
}

it("changes a managed resource account across multiple accounts on the same cloud and saves target via manifest PUT", async () => {
  const save = vi.fn()
  const edit = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      services: { api: { source: "cmd/api", needs: ["postgres"] } },
      resources: { postgres: { kind: "postgres", provider: "digitalocean" } },
    },
    onSave: save,
    onEdit: edit,
  })
  const { onSaved, replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Change placement" }))
  await user.click(await screen.findByRole("combobox", { name: "Account for postgres" }))
  await user.click(await screen.findByRole("option", { name: "Digital Ocean Wakflo" }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledWith(["api"]))
  expect(edit).not.toHaveBeenCalled()
  expect(save).toHaveBeenCalledOnce()
  expect(save.mock.calls[0][0]).toMatchObject({
    base_revision: 11,
    manifest: {
      resources: {
        postgres: {
          kind: "postgres",
          provider: "digitalocean",
          environments: { production: { target: "conn_do_2" } },
        },
      },
    },
  })
  expect(onSaved).toHaveBeenCalledOnce()
})

it("resets top-level resource target back to environment default and preserves other environments", async () => {
  const save = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      resources: { postgres: { kind: "postgres", provider: "digitalocean", target: "conn_do_1" } },
    },
    onSave: save,
  })
  const { replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Change placement" }))
  await user.click(await screen.findByRole("combobox", { name: "Account for postgres" }))
  await user.click(await screen.findByRole("option", { name: "Environment default" }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledOnce())
  expect(save).toHaveBeenCalledOnce()
  const savedManifest = (save.mock.calls[0][0] as { manifest: Manifest }).manifest
  expect(savedManifest.resources?.postgres.target).toBeUndefined()
  expect(savedManifest.resources?.postgres.environments?.staging?.target).toBe("conn_do_1")
  expect(savedManifest.resources?.postgres.environments?.production?.target).toBeUndefined()
})

it("switches a resource to an owned server target via manifest PUT", async () => {
  const save = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      resources: { postgres: { kind: "postgres", provider: "digitalocean" } },
    },
    onSave: save,
  })
  const { replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Change placement" }))
  await user.click(await screen.findByRole("combobox", { name: "Target for postgres" }))
  await user.click(await screen.findByRole("option", { name: "Server" }))
  await user.click(await screen.findByRole("combobox", { name: "Infrastructure for postgres" }))
  await user.click(await screen.findByRole("option", { name: "Host A" }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledOnce())
  expect(save.mock.calls[0][0]).toMatchObject({
    manifest: {
      resources: {
        postgres: {
          environments: { production: { provider: "server", target: "conn_srv_1" } },
        },
      },
    },
  })
})

it("shows per-environment toggle for existing_resource placement and writes opt-in environments", async () => {
  const save = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      services: { api: { source: "cmd/api", needs: ["shared-db"] } },
      resources: {
        "shared-db": { kind: "postgres", existing_resource: "providerresource_1" },
      },
    },
    onSave: save,
  })
  const { replan } = renderActions("shared-db")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage shared-db" }))
  await user.click(await screen.findByRole("menuitem", { name: "Change placement" }))
  expect(screen.queryByRole("combobox", { name: "Target for shared-db" })).not.toBeInTheDocument()
  await user.click(await screen.findByRole("switch", { name: "Enable shared-db in production" }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledWith(["api"]))
  expect(save.mock.calls[0][0]).toMatchObject({
    manifest: {
      resources: {
        "shared-db": {
          kind: "postgres",
          existing_resource: "providerresource_1",
          environments: {
            staging: { enabled: true },
            production: { enabled: false },
          },
        },
      },
    },
  })
})

it("modifies version, capacity size, and consumer wiring in one manifest revision", async () => {
  const save = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      services: {
        api: { source: "cmd/api", needs: ["postgres"] },
        worker: { source: "cmd/worker" },
      },
      resources: {
        postgres: { kind: "postgres", provider: "digitalocean", version: "16" },
      },
    },
    onSave: save,
  })
  const { replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Modify settings" }))

  await user.click(await screen.findByRole("combobox", { name: "Version" }))
  await user.click(await screen.findByRole("option", { name: "17" }))

  const sizeSelect = await screen.findByLabelText(/Size/)
  await user.selectOptions(sizeSelect, "db-s-2vcpu-4gb")

  await user.click(screen.getByRole("checkbox", { name: /worker/ }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledWith(["api", "worker"]))
  expect(save.mock.calls[0][0]).toMatchObject({
    base_revision: 11,
    manifest: {
      services: {
        api: { needs: ["postgres"] },
        worker: { needs: ["postgres"] },
      },
      resources: {
        postgres: {
          version: "17",
          environments: {
            production: { settings: { size: "db-s-2vcpu-4gb", nodes: "1" } },
          },
        },
      },
    },
  })
})

it("lets existing_resource update Used by wiring while noting external management", async () => {
  const save = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      services: { api: { source: "cmd/api" } },
      resources: {
        "shared-db": { kind: "postgres", existing_resource: "providerresource_1" },
      },
    },
    onSave: save,
  })
  const { replan } = renderActions("shared-db")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage shared-db" }))
  await user.click(await screen.findByRole("menuitem", { name: "Modify settings" }))
  expect(await screen.findByText(/Version and capacity are managed by the external provider/)).toBeInTheDocument()
  await user.click(screen.getByRole("checkbox", { name: /api/ }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledWith(["api"]))
  expect(save.mock.calls[0][0]).toMatchObject({
    manifest: { services: { api: { needs: ["shared-db"] } } },
  })
})

it("replaces a resource with another project resource", async () => {
  const save = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      services: { api: { source: "cmd/api", needs: ["postgres"] } },
      resources: {
        postgres: { kind: "postgres", provider: "digitalocean" },
        "postgres-2": { kind: "postgres", provider: "digitalocean" },
      },
    },
    onSave: save,
  })
  const { replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Use existing resource" }))
  await user.click(await screen.findByRole("combobox", { name: "Existing resource" }))
  await user.click(await screen.findByRole("option", { name: "postgres-2 · this project" }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledWith(["api"]))
  const savedManifest = (save.mock.calls[0][0] as { manifest: Manifest }).manifest
  expect(savedManifest.resources?.postgres).toBeUndefined()
  expect(savedManifest.services?.api.needs).toEqual(["postgres-2"])
})

it("replaces a resource with a verified workspace resource and notes unready ones", async () => {
  const save = vi.fn()
  const now = new Date().toISOString()
  setupFetch({
    manifest: {
      version: 1,
      services: { api: { source: "cmd/api", needs: ["postgres"] } },
      resources: { postgres: { kind: "postgres", provider: "digitalocean" } },
    },
    providerResources: [
      {
        id: "providerresource_ready",
        name: "prod-pg",
        kind: "postgres",
        provider: "digitalocean",
        region: "nyc3",
        endpointId: "ep_1",
        connectionState: "ready",
        observationState: "observed",
        observedAt: now,
        consumers: [],
      },
      {
        id: "providerresource_unready",
        name: "stale-pg",
        kind: "postgres",
        provider: "digitalocean",
        region: "nyc3",
        endpointId: "",
        connectionState: "identity_verification_needed",
        observationState: "observed",
        observedAt: now,
        consumers: [],
      },
    ],
    onSave: save,
  })
  const { replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Use existing resource" }))
  expect(await screen.findByText(/1 compatible workspace resource needs sync or verified credentials/)).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Integrations" })).toHaveAttribute("href", "/integrations")

  await user.click(screen.getByRole("combobox", { name: "Existing resource" }))
  await user.click(await screen.findByRole("option", { name: "prod-pg · digitalocean · nyc3" }))
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  await waitFor(() => expect(replan).toHaveBeenCalledWith(["api"]))
  const savedManifest = (save.mock.calls[0][0] as { manifest: Manifest }).manifest
  expect(savedManifest.resources?.postgres).toBeUndefined()
  expect(savedManifest.resources?.["postgres-existing"]).toEqual({
    kind: "postgres",
    existing_resource: "providerresource_ready",
  })
  expect(savedManifest.services?.api.needs).toEqual(["postgres-existing"])
})

it("does not query workspace inventory without infrastructure:read permission", async () => {
  canPermission = (perm) => perm !== "infrastructure:read"
  const fetchedInventory = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      resources: {
        postgres: { kind: "postgres", provider: "digitalocean" },
        "postgres-2": { kind: "postgres", provider: "digitalocean" },
      },
    },
    onProviderResources: fetchedInventory,
  })
  renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Use existing resource" }))
  await user.click(await screen.findByRole("combobox", { name: "Existing resource" }))
  expect(await screen.findByRole("option", { name: "postgres-2 · this project" })).toBeInTheDocument()
  expect(fetchedInventory).not.toHaveBeenCalled()
})

it("removes a resource with optional ignore and refreshes manifest on 409 conflict", async () => {
  const save = vi.fn()
  const manifestGets = vi.fn()
  setupFetch({
    manifest: {
      version: 1,
      services: { api: { source: "cmd/api", needs: ["postgres"] } },
      resources: { postgres: { kind: "postgres", provider: "digitalocean" } },
    },
    saveStatus: 409,
    onSave: save,
    onManifestGet: manifestGets,
  })
  const { replan } = renderActions("postgres")
  const user = userEvent.setup()

  await user.click(screen.getByRole("button", { name: "Manage postgres" }))
  await user.click(await screen.findByRole("menuitem", { name: "Remove from manifest" }))
  await user.click(await screen.findByRole("checkbox", { name: /Also ignore Postgres/ }))
  const getsBefore = manifestGets.mock.calls.length
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))

  expect(await screen.findByRole("alert")).toHaveTextContent("Someone saved the manifest at the same time.")
  await waitFor(() => expect(manifestGets.mock.calls.length).toBeGreaterThan(getsBefore))
  expect(save.mock.calls[0][0]).toMatchObject({
    manifest: {
      ignore: { resources: ["postgres"] },
      services: { api: { source: "cmd/api" } },
    },
  })
  expect(replan).not.toHaveBeenCalled()
})
