import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import {
  DeployTab,
  FindingActions,
  ManifestEditor,
  ManifestSettings,
} from "@/components/deploy"
import { DeploymentForm } from "@/components/deploy-dialog"
import { PlacementPanel } from "@/components/deployment-targets"
import type {
  Environment,
  Finding,
  Manifest,
  Project,
} from "@/client/forge/loomplane/types"
import type { PlacementRow } from "@/lib/placement"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: {} }),
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const service = (
  m: Manifest,
  name: string,
  kind = "http-service"
): PlacementRow => ({
  section: "services",
  name,
  kind,
  entry: m.services![name],
})

function panel(
  m: Manifest,
  row: PlacementRow,
  environment = "production",
  onClose = vi.fn()
) {
  const view = (r: PlacementRow) => (
    <ClientProvider client={forgeClient}>
      <PlacementPanel
        projectId="p"
        manifest={m}
        revision={1}
        row={r}
        environment={environment}
        environments={[{ id: "env", name: environment } as Environment]}
        mode="save"
        onClose={onClose}
      />
    </ClientProvider>
  )
  const { rerender } = render(view(row))
  return { onClose, show: (r: PlacementRow) => rerender(view(r)) }
}

const openBuild = async () =>
  userEvent.click(
    await screen.findByRole("button", { name: "Build and network" })
  )

it("offers managed Kubernetes builds without separate connection selections", async () => {
  const manifest: Manifest = {
    version: 1,
    services: {
      api: {
        source: "go:cmd/api",
        provider: "kubernetes",
        target: "conn_cluster",
        public: false,
      },
    },
  }
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.endsWith("/providers"))
      return json({
        providers: [
          {
            name: "kubernetes",
            title: "Kubernetes",
            cloud: "kubernetes",
            available: true,
            runs: [{ workload: "http-service" }],
          },
        ],
      })
    if (url.endsWith("/connections"))
      return json({
        connections: [
          {
            id: "conn_cluster",
            name: "Cluster",
            cloud: "kubernetes",
            status: "ready",
            details: { registry_hostname: "images.example.com" },
          },
        ],
      })
    return json({})
  })
  panel(manifest, service(manifest, "api"))
  await openBuild()
  expect(
    await screen.findByText(
      /Builds run in this cluster and publish to images.example.com/
    )
  ).toBeVisible()
  expect(
    screen.getByRole("combobox", { name: "Builder for api" })
  ).toHaveTextContent("Build in cluster")
  expect(
    screen.getByRole("combobox", { name: "Registry for api" })
  ).toHaveTextContent("Managed cluster registry")
  await userEvent.type(
    screen.getByRole("textbox", { name: "Image for api" }),
    "public.example.com/api@sha256:" + "a".repeat(64)
  )
  expect(
    screen.queryByText(/Builds run in this cluster/)
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("combobox", { name: "Builder for api" })
  ).toBeDisabled()
})

it("saves an owned target, builder and registry while preserving the build context", async () => {
  const manifest: Manifest = {
    version: 1,
    services: {
      api: {
        source: "go:cmd/api",
        provider: "server",
        build: { context: "workspace" },
      },
      second: { source: "go:cmd/second", provider: "server" },
    },
  }
  const writes: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.body) writes.push(JSON.parse(String(init.body)))
      if (url.endsWith("/providers"))
        return json({
          providers: [
            {
              name: "server",
              title: "Existing server",
              cloud: "server",
              available: true,
              runs: [{ workload: "http-service" }],
            },
          ],
        })
      if (url.endsWith("/connections"))
        return json({
          connections: [
            {
              id: "conn_host",
              name: "Host",
              cloud: "server",
              status: "ready",
              details: {},
            },
            {
              id: "conn_builder",
              name: "Builder",
              cloud: "server",
              status: "ready",
              details: { build_workers: "dedicated" },
            },
            {
              id: "conn_registry",
              name: "Registry",
              cloud: "registry",
              status: "ready",
              details: {},
            },
          ],
        })
      return json({ id: "r2", revision: 2 })
    }
  )
  const { onClose, show } = panel(manifest, service(manifest, "api"))
  await userEvent.click(
    await screen.findByRole("combobox", { name: "Infrastructure for api" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "Host" }))
  await openBuild()
  await userEvent.click(
    screen.getByRole("combobox", { name: "Builder for api" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "Builder" }))
  await userEvent.click(
    screen.getByRole("combobox", { name: "Registry for api" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "Registry" }))
  await userEvent.type(
    screen.getByRole("spinbutton", { name: "Host port for api" }),
    "8088"
  )
  await userEvent.click(screen.getByRole("button", { name: "Save placement" }))
  await vi.waitFor(() => expect(onClose).toHaveBeenCalled())
  expect(writes).toContainEqual({
    placement: expect.objectContaining({
      name: "api",
      environment: "production",
      target: "conn_host",
      build: {
        context: "workspace",
        builder: "conn_builder",
        registry: "conn_registry",
      },
      endpoint: { host_port: 8088 },
    }),
  })
  show(service(manifest, "second"))
  await userEvent.click(
    await screen.findByRole("combobox", { name: "Infrastructure for second" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "Builder" }))
  await userEvent.click(screen.getByRole("button", { name: "Save placement" }))
  await vi.waitFor(() =>
    expect(writes).toContainEqual({
      placement: expect.objectContaining({
        name: "second",
        target: "conn_builder",
      }),
    })
  )
}, 15_000)

it("keeps unsaved YAML when a placement revision arrives and prevents overwriting it", async () => {
  const project = { id: "p" } as Project
  const view = (yaml: string) => (
    <ClientProvider client={forgeClient}>
      <ManifestEditor project={project} yaml={yaml} revision={1} />
    </ClientProvider>
  )
  const { rerender } = render(view("version: 1\n"))
  await userEvent.type(
    screen.getByRole("textbox", { name: "Manifest" }),
    "# Local notes"
  )
  rerender(view("# Another saved placement\nversion: 1\n"))
  expect(screen.getByRole("textbox", { name: "Manifest" })).toHaveValue(
    "version: 1\n# Local notes"
  )
  expect(screen.getByRole("button", { name: "Save revision" })).toBeDisabled()
  expect(screen.getByRole("alert")).toHaveTextContent(
    "A newer manifest revision was saved"
  )
  await userEvent.click(screen.getByRole("button", { name: "Undo changes" }))
  expect(screen.getByRole("textbox", { name: "Manifest" })).toHaveValue(
    "# Another saved placement\nversion: 1\n"
  )
})

it("shows failed setup reads instead of silently hiding infrastructure choices", async () => {
  vi.stubGlobal("fetch", async () =>
    json({ error: "Target catalog unavailable" }, 503)
  )
  const m: Manifest = {
    version: 1,
    services: { api: { source: "go:cmd/api", provider: "render" } },
  }
  panel(m, service(m, "api"), "preview")
  expect(
    await screen.findByRole("alert", {}, { timeout: 5000 })
  ).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
})

it("asks which account deploys a cloud and saves it with the placement", async () => {
  const writes: { url: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.body) writes.push({ url, body: JSON.parse(String(init.body)) })
      if (url.endsWith("/providers"))
        return json({
          providers: [
            {
              name: "render",
              title: "Render",
              cloud: "render",
              available: true,
              runs: [{ workload: "worker" }],
            },
          ],
        })
      if (url.endsWith("/preview/connections"))
        return json({ environment: "preview", connections: {} })
      if (url.endsWith("/connections"))
        return json({
          connections: [
            {
              id: "c1",
              name: "Team",
              account: "team",
              cloud: "render",
              status: "ready",
              details: {},
            },
            {
              id: "c2",
              name: "Personal",
              account: "me",
              cloud: "render",
              status: "ready",
              details: {},
            },
          ],
        })
      return json({})
    }
  )
  const m: Manifest = {
    version: 1,
    services: { worker: { source: "go:cmd/worker", provider: "render" } },
  }
  panel(m, service(m, "worker", "worker"), "preview")
  await userEvent.click(
    await screen.findByRole("combobox", {
      name: "Account for Render in preview",
    })
  )
  await userEvent.click(
    await screen.findByRole("option", { name: "Personal · me" })
  )
  await userEvent.click(screen.getByRole("button", { name: "Save placement" }))
  await vi.waitFor(() =>
    expect(writes).toEqual([
      {
        url: expect.stringContaining(
          "/projects/p/environments/preview/connections/render"
        ),
        body: { connection_id: "c2" },
      },
    ])
  )
})

it("offers compatible managed targets and saves a scoped placement without changing source", async () => {
  const m: Manifest = {
    version: 1,
    services: { worker: { source: "go:cmd/worker", provider: "render" } },
  }
  let edit: unknown
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith("/providers"))
        return json({
          providers: [
            {
              name: "vercel",
              title: "Vercel",
              cloud: "vercel",
              available: true,
              runs: [{ workload: "static-site" }],
            },
            {
              name: "render",
              title: "Render",
              cloud: "render",
              available: true,
              runs: [{ workload: "worker" }],
            },
            {
              name: "ecs-fargate",
              title: "AWS ECS",
              cloud: "aws",
              available: true,
              runs: [{ workload: "worker" }],
              regions: ["us-east-1"],
            },
          ],
        })
      if (url.endsWith("/manifest/edits")) {
        edit = JSON.parse(String(init?.body))
        return json({ id: "r2", revision: 2 })
      }
      return json({ connections: [] })
    }
  )
  const { onClose } = panel(m, service(m, "worker", "worker"), "preview")
  await userEvent.click(
    await screen.findByRole("combobox", { name: "Target for worker" })
  )
  const list = await screen.findByRole("listbox")
  expect(
    within(list).queryByRole("option", { name: "Vercel" })
  ).not.toBeInTheDocument()
  await userEvent.click(within(list).getByRole("option", { name: "AWS ECS" }))
  await userEvent.type(screen.getByLabelText("Region for worker"), "us-east-1")
  await userEvent.click(screen.getByRole("button", { name: "Save placement" }))
  await vi.waitFor(() => expect(onClose).toHaveBeenCalled())
  expect(edit).toEqual({
    placement: {
      section: "services",
      name: "worker",
      environment: "preview",
      provider: "ecs-fargate",
      region: "us-east-1",
    },
  })
  expect(m.services?.worker.source).toBe("go:cmd/worker")
})

// A project with one environment, one worker and one finished deploy.
function deployTabAPI({
  edit,
  bound = false,
  save,
  removeResource,
}: {
  edit?: Promise<Response>
  removeResource?: (body: unknown) => void
  bound?: boolean
  save?: (body: unknown) => void
} = {}) {
  const manifest: Manifest = {
    version: 1,
    ...(removeResource && {
      resources: { resource: { kind: "postgres", enabled: false } },
    }),
    services: { worker: { source: "go:cmd/worker", provider: "render" } },
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith("/manifest/edits") && removeResource) {
        removeResource(JSON.parse(String(init?.body)))
        return json({
          yaml: "version: 1\nservices: {worker: {source: go:cmd/worker}}\n",
        })
      }
      if (url.endsWith("/manifest/edits") && edit) return edit
      if (url.endsWith("/manifest") && init?.method === "PUT")
        save?.(JSON.parse(String(init.body)))
      if (url.endsWith("/manifest"))
        return json({ id: "r1", revision: 1, yaml: "version: 1\n", manifest })
      if (url.endsWith("/graph"))
        return json({
          project_id: "p",
          graph: {
            services: [
              {
                id: "svc:go:cmd/worker",
                entrypoint: "cmd/worker",
                path: ".",
                kind: "worker",
                deployable: true,
              },
            ],
            sources: [],
          },
        })
      if (url.endsWith("/providers"))
        return json({
          providers: [
            {
              name: "render",
              title: "Render",
              cloud: "render",
              available: true,
              runs: [{ workload: "worker" }],
            },
          ],
        })
      if (url.endsWith("/sources") || url.endsWith("/deployed"))
        return json({ sources: [] })
      if (url.endsWith("/plans") && init?.method !== "POST")
        return json({
          plans: [
            {
              id: "plan_1",
              environment: "preview",
              status: "approved",
              manifest_revision: 1,
              created_at: "2026-11-01T10:00:00Z",
              sources: [{ alias: "app", commit_sha: "3f2a1c9aa" }],
              problems: [],
              warnings: [],
              actions: [],
              summary: { create: 1, replace: 2, keep: 1, update: 0, delete: 0 },
            },
          ],
        })
      if (url.endsWith("/deployments"))
        return json({
          deployments: [
            {
              id: "d1",
              plan_id: "plan_1",
              environment: "preview",
              status: "succeeded",
              started_at: "2026-11-01T10:05:00Z",
            },
          ],
        })
      if (url.endsWith("/projects"))
        return json({ projects: [{ id: "p", name: "Example" }] })
      if (url.endsWith("/environments"))
        return json({ environments: [{ id: "e", name: "preview" }] })
      if (url.includes("/bindings"))
        return json({
          bindings: bound ? [{ subject: "service", name: "worker" }] : [],
        })
      return json({ connections: [] })
    }
  )
}

it("lists the project's deployments with no way to approve them here", async () => {
  deployTabAPI()
  render(
    <ClientProvider client={forgeClient}>
      <DeployTab project={{ id: "p", name: "Example" } as Project} />
    </ClientProvider>
  )
  const table = await screen.findByRole("table", { name: "Deployments" })
  expect(await within(table).findByText("Deployed")).toBeInTheDocument()
  expect(within(table).getByText("2 replace, 1 create, 1 kept")).toBeVisible()
  expect(within(table).getByRole("link", { name: "Open" })).toHaveAttribute(
    "href",
    "/projects/$id/deployments/$runId"
  )
  expect(
    screen.queryByRole("button", { name: /Approve|Reject/ })
  ).not.toBeInTheDocument()
  expect(
    await screen.findByRole("table", { name: "Where services run in preview" })
  ).toHaveTextContent("Render")
})

it("holds plans while a placement saved from the Deployments tab is in flight", async () => {
  let finishSave: (response: Response) => void = () => {}
  deployTabAPI({
    edit: new Promise<Response>((resolve) => {
      finishSave = resolve
    }),
  })
  render(
    <ClientProvider client={forgeClient}>
      <>
        <DeployTab project={{ id: "p", name: "Example" } as Project} />
        <DeploymentForm
          preset={{ project: "p", environment: "preview" }}
          done={() => {}}
        />
      </>
    </ClientProvider>
  )
  const review = await screen.findByRole(
    "button",
    { name: "Review plan" },
    { timeout: 5000 }
  )
  await vi.waitFor(() => expect(review).toBeEnabled(), { timeout: 5000 })
  // The Deployments tab's own row, not the deploy screen's.
  const placed = await screen.findByRole(
    "table",
    { name: "Where services run in preview" },
    { timeout: 5000 }
  )
  const change = within(placed).getByRole("button", {
    name: "Change where worker runs",
  })
  await userEvent.click(change)
  expect(
    await screen.findByText(
      "Saving makes this preview's standing setting. Nothing moves until the next deploy."
    )
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("switch", { name: "Enable worker in preview" })
  )
  expect(review).toBeDisabled()
  await userEvent.click(screen.getByRole("button", { name: "Save placement" }))
  expect(review).toBeDisabled()
  finishSave(json({ id: "r2", revision: 2 }))
  await vi.waitFor(() => expect(review).toBeEnabled(), { timeout: 5000 })
}, 15_000)

it("shows loomplane.yaml's revision and opens the editor on request", async () => {
  deployTabAPI()
  render(
    <ClientProvider client={forgeClient}>
      <ManifestSettings project={{ id: "p", name: "Example" } as Project} />
    </ClientProvider>
  )
  expect(await screen.findByText("revision 1")).toBeInTheDocument()
  expect(
    screen.queryByRole("textbox", { name: "Manifest" })
  ).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Edit loomplane.yaml" })
  )
  expect(screen.getByRole("textbox", { name: "Manifest" })).toHaveValue(
    "version: 1\n"
  )
})

it("offers a finding's fix for the deployment page to show", async () => {
  const writes: unknown[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.body) writes.push(JSON.parse(String(init.body)))
      if (url.endsWith("/manifest"))
        return json({
          id: "r1",
          revision: 1,
          yaml: "version: 1\n",
          manifest: { version: 1, services: {} },
        })
      if (url.endsWith("/providers")) return json({ providers: [] })
      return json({ id: "r2", revision: 2 })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <FindingActions
        finding={
          { code: "unprovided-resource", name: "redis", message: "" } as Finding
        }
        project={{ id: "p" } as Project}
        environment="preview"
      />
    </ClientProvider>
  )
  // Nothing provisions redis, so only ignoring it is offered.
  await userEvent.click(
    await screen.findByRole("button", { name: /doesn't use/ })
  )
  await vi.waitFor(() => expect(writes).toEqual([{ ignore: "redis" }]))
})

it("lists every Cloud's targets and puts a service on another Cloud's", async () => {
  const manifest: Manifest = {
    version: 1,
    services: { web: { source: "node:web", cloud_target: "frontend" } },
  }
  const saves: Record<string, unknown>[] = []
  const target = (id: string, name: string, cloud: string) => ({
    id,
    name,
    display_name: name.toUpperCase(),
    cloud_id: cloud,
    provider: "vercel",
    connection_id: "conn",
    revision: 1,
  })
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.method === "PUT") {
        saves.push(JSON.parse(String(init.body)))
        return json({ id: "rev", revision: 2, manifest })
      }
      if (url.endsWith("/providers"))
        return json({
          providers: [
            {
              name: "vercel",
              title: "Vercel",
              cloud: "vercel",
              available: true,
              runs: [{ workload: "http-service" }],
            },
          ],
        })
      if (url.endsWith("/default-cloud"))
        return json({
          project_id: "p",
          revision: 1,
          cloud: {
            cloud_id: "main",
            name: "Main",
            default: true,
            targets: [target("t1", "frontend", "main")],
          },
        })
      if (url.endsWith("/projects/p/clouds"))
        return json({
          clouds: [
            { id: "main", name: "Main" },
            { id: "lab", name: "GPU Lab" },
          ],
        })
      if (url.endsWith("/clouds/lab/targets"))
        return json({ targets: [target("t2", "a100", "lab")] })
      return json({})
    }
  )
  panel(manifest, service(manifest, "web"))
  await userEvent.click(
    await screen.findByRole("combobox", { name: "Target for web" })
  )
  // Each Cloud's targets sit under its own name.
  const lab = await screen.findByRole("group", { name: "GPU Lab" })
  expect(screen.getByRole("group", { name: "Main" })).toHaveTextContent(
    "FRONTEND"
  )
  await userEvent.click(within(lab).getByRole("option", { name: "A100" }))
  expect(
    screen.getByRole("combobox", { name: "Target for web" })
  ).toHaveTextContent("a100 in GPU Lab")
  await userEvent.click(screen.getByRole("button", { name: "Save placement" }))
  await vi.waitFor(() => expect(saves).toHaveLength(1))
  expect(saves[0]).toMatchObject({
    manifest: {
      services: {
        web: {
          environments: {
            production: { cloud: "GPU Lab", cloud_target: "a100" },
          },
        },
      },
    },
  })
})

it.each([true, false])(
  "checks bindings before removing a manifest entry (bound %s)",
  async (bound) => {
    const save = vi.fn()
    deployTabAPI({ bound, save })
    render(
      <ClientProvider client={forgeClient}>
        <DeployTab project={{ id: "p", name: "Example" } as Project} />
      </ClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole("button", { name: "More actions for worker" })
    )
    await user.click(
      await screen.findByRole("menuitem", { name: "Remove from manifest" })
    )
    await user.click(
      await screen.findByRole("button", { name: "Remove from manifest" })
    )
    if (bound) {
      expect(await screen.findByRole("alert")).toHaveTextContent(
        "has a deployment binding"
      )
      expect(save).not.toHaveBeenCalled()
    } else {
      await vi.waitFor(() =>
        expect(save).toHaveBeenCalledExactlyOnceWith({
          base_revision: 1,
          manifest: { version: 1, services: {} },
        })
      )
    }
  }
)

it("removes the manually added database through a guarded manifest revision", async () => {
  const save = vi.fn()
  const removeResource = vi.fn()
  deployTabAPI({ save, removeResource })
  render(
    <ClientProvider client={forgeClient}>
      <DeployTab project={{ id: "p", name: "Example" } as Project} />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "More actions for resource" })
  )
  await user.click(
    await screen.findByRole("menuitem", { name: "Remove from manifest" })
  )
  await user.click(
    await screen.findByRole("button", { name: "Remove from manifest" })
  )
  await vi.waitFor(() =>
    expect(save).toHaveBeenCalledExactlyOnceWith({
      base_revision: 1,
      yaml: "version: 1\nservices: {worker: {source: go:cmd/worker}}\n",
    })
  )
  expect(removeResource).toHaveBeenCalledExactlyOnceWith({
    dry_run: true,
    remove_resource: "resource",
  })
})

it("attaches an existing workspace resource from an unprovided-resource finding", async () => {
  const writes: unknown[] = []
  const replan = vi.fn().mockResolvedValue(undefined)
  const now = new Date().toISOString()
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith("/manifest/edits") && init?.method === "POST") {
        writes.push(JSON.parse(String(init.body)))
        return json({ id: "r2", revision: 2 })
      }
      if (url.endsWith("/manifest")) {
        return json({
          id: "r1",
          revision: 1,
          yaml: "version: 1\n",
          manifest: {
            version: 1,
            services: { api: { source: "cmd/api" } },
          },
        })
      }
      if (url.endsWith("/providers")) return json({ providers: [] })
      if (url.endsWith("/provider-resources")) {
        return json({
          resources: [
            {
              id: "providerresource_pg",
              name: "Prod PG",
              kind: "postgres",
              provider: "digitalocean",
              region: "nyc3",
              endpointId: "ep_1",
              connectionState: "ready",
              observationState: "observed",
              observedAt: now,
            },
          ],
        })
      }
      return json({})
    }
  )

  render(
    <ClientProvider client={forgeClient}>
      <FindingActions
        finding={
          {
            code: "unprovided-resource",
            name: "postgres",
            message: "",
          } as Finding
        }
        project={{ id: "p", name: "Shop" } as Project}
        environment="production"
        replan={replan}
      />
    </ClientProvider>
  )

  const user = userEvent.setup()
  await user.click(await screen.findByRole("button", { name: "Use existing" }))
  await user.click(
    await screen.findByRole("combobox", { name: "Existing resource" })
  )
  await user.click(
    await screen.findByRole("option", { name: "Prod PG · digitalocean · nyc3" })
  )
  await user.click(screen.getByRole("checkbox", { name: /api/ }))
  await user.click(
    screen.getByRole("button", { name: "Attach and plan again" })
  )

  await vi.waitFor(() => expect(replan).toHaveBeenCalledOnce())
  expect(writes).toEqual([
    {
      add_resource: {
        name: "prod-pg",
        kind: "postgres",
        existing_resource: "providerresource_pg",
        needed_by: ["api"],
      },
    },
  ])
})

it("displays Existing resource for external attachments and exposes resource menu actions in WhereServicesRun", async () => {
  const saves: unknown[] = []
  const manifest: Manifest = {
    version: 1,
    services: { worker: { source: "go:cmd/worker", provider: "render" } },
    resources: {
      "shared-db": {
        kind: "postgres",
        existing_resource: "providerresource_1",
      },
    },
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith("/manifest") && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { manifest: Manifest }
        saves.push(body)
        return json({
          id: "r2",
          revision: 2,
          manifest: body.manifest,
          yaml: "",
        })
      }
      if (url.endsWith("/manifest")) {
        return json({ id: "r1", revision: 1, yaml: "version: 1\n", manifest })
      }
      if (url.endsWith("/environments")) {
        return json({ environments: [{ id: "e1", name: "production" }] })
      }
      if (url.endsWith("/graph")) {
        return json({
          project_id: "p",
          graph: {
            services: [
              {
                id: "svc:go:cmd/worker",
                entrypoint: "cmd/worker",
                path: ".",
                kind: "worker",
                deployable: true,
              },
            ],
            sources: [],
          },
        })
      }
      if (url.endsWith("/providers")) return json({ providers: [] })
      if (url.endsWith("/connections")) return json({ connections: [] })
      return json({})
    }
  )

  render(
    <ClientProvider client={forgeClient}>
      <DeployTab project={{ id: "p", name: "Example" } as Project} />
    </ClientProvider>
  )

  const table = await screen.findByRole("table", {
    name: "Where services run in production",
  })
  expect(within(table).getByText("Existing resource")).toBeInTheDocument()
  expect(within(table).getByText("Managed externally")).toBeInTheDocument()

  const user = userEvent.setup()
  await user.click(
    within(table).getByRole("button", { name: "More actions for shared-db" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Modify settings" })
  ).toBeInTheDocument()
  expect(
    screen.getByRole("menuitem", { name: "Use existing resource" })
  ).toBeInTheDocument()
  expect(
    screen.getByRole("menuitem", { name: "Remove from manifest" })
  ).toBeInTheDocument()
  await user.keyboard("{Escape}")

  await user.click(
    within(table).getByRole("button", { name: "Change where shared-db runs" })
  )
  await user.click(
    await screen.findByRole("switch", {
      name: "Enable shared-db in production",
    })
  )
  await user.click(screen.getByRole("button", { name: "Save manifest" }))

  await vi.waitFor(() => expect(saves).toHaveLength(1))
  expect(saves[0]).toMatchObject({
    base_revision: 1,
    manifest: {
      resources: {
        "shared-db": {
          kind: "postgres",
          existing_resource: "providerresource_1",
          environments: { production: { enabled: false } },
        },
      },
    },
  })
})
