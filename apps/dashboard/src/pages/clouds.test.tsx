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
import { CloudsWorkspace } from "./clouds"
import { ChangeReview } from "@/components/cloud-targets/change-review"
import { SyncHistory } from "@/components/cloud-targets/sync-history"

let denied = false
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => !denied }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  window.localStorage.clear()
  denied = false
})
const profile = {
  id: "cloud",
  name: "Production",
  revision: 1,
  project_id: "p",
  org_id: "org",
}
const target = {
  id: "target",
  name: "frontend",
  display_name: "Frontend",
  cloud_id: "cloud",
  provider: "vercel",
  connection_id: "a",
  region: "",
  revision: 1,
}
const env = {
  environment_id: "prod",
  environment: "production",
  services: ["web"],
  status: "failed",
  error: "Readiness failed",
  summary: { create: 0, update: 0, replace: 1, delete: 0, adopt: 0, keep: 0 },
  external_routing: false,
}
const preview = {
  project_id: "p",
  org_id: "org",
  id: "preview",
  revision: "digest",
  input: { cloud_id: "cloud", revision: 1 },
  before: { ...target, connection_id: "a" },
  after: { ...target, connection_id: "b" },
  environments: [env],
  blockers: [],
}
function fixture(
  options: {
    failure?: string
    noClouds?: boolean
    noEnvironments?: boolean
    /** One Cloud over two DigitalOcean accounts and a Vercel one. */
    spread?: boolean
  } = {}
) {
  const writes: { url: string; body: Record<string, unknown> }[] = []
  const reads: string[] = []
  let profiles = options.noClouds ? [] : [profile]
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const path = new URL(url, "http://localhost").pathname
      const method = init?.method ?? "GET"
      reads.push(url)
      if (method !== "GET") {
        const payload = JSON.parse(String(init?.body ?? "{}"))
        writes.push({ url, body: payload })
        if (method === "DELETE" && options.failure === "delete")
          return Response.json(
            { message: "This cloud or target is assigned to an environment." },
            { status: 409 }
          )
        if (path.endsWith("/clouds")) {
          profiles = [{ ...profile, name: payload.name }]
          return Response.json(profiles[0], { status: 201 })
        }
        if (path.endsWith("/preview")) return Response.json(preview)
        if (path.endsWith("/apply") && options.failure)
          return Response.json({ message: options.failure }, { status: 409 })
        return Response.json({ id: "sync", status: "pending" })
      }
      if (
        options.failure === "list" &&
        path.includes("/projects/p/") &&
        path.endsWith("/clouds")
      )
        return Response.json({ message: "Cloud read failed" }, { status: 503 })
      let body: unknown = {}
      if (path.endsWith("/projects"))
        body = {
          projects: [
            { id: "p", name: "App" },
            { id: "other", name: "Other project" },
          ],
        }
      if (
        options.failure === "environments" &&
        path.includes("/projects/p/environments")
      )
        return Response.json(
          { message: "Environment read failed" },
          { status: 503 }
        )
      if (path.endsWith("/environments"))
        body = {
          environments:
            options.noEnvironments && path.includes("/projects/p/")
              ? []
              : [
                  { id: "prod", name: "production" },
                  { id: "stage", name: "staging" },
                ],
        }
      if (path.endsWith("/clouds")) body = { clouds: profiles }
      if (path.endsWith("/cloud")) body = { assignment: null, revision: 0 }
      if (path.endsWith("/targets"))
        body = {
          targets: options.spread
            ? [
                target,
                {
                  ...target,
                  id: "api",
                  name: "api",
                  display_name: "API",
                  provider: "digitalocean",
                  connection_id: "do-1",
                  region: "nyc3",
                },
                {
                  ...target,
                  id: "worker",
                  name: "worker",
                  display_name: "Worker",
                  provider: "digitalocean",
                  connection_id: "do-2",
                  region: "ams3",
                },
              ]
            : [target],
        }
      if (path.endsWith("/targets/target")) body = target
      if (path.endsWith("/connections"))
        body = {
          connections: [
            {
              id: "a",
              name: "Account A",
              cloud: "vercel",
              account: "Team A",
              status: "ready",
            },
            ...(options.spread
              ? [
                  {
                    id: "do-1",
                    name: "Production DO",
                    cloud: "digitalocean",
                    account: "prod-team",
                    status: "ready",
                  },
                  {
                    id: "do-2",
                    name: "EU DO",
                    cloud: "digitalocean",
                    account: "eu-team",
                    status: "ready",
                  },
                ]
              : []),
          ],
        }
      if (path.endsWith("/cloud-syncs")) body = { syncs: [] }
      return Response.json(body)
    }
  )
  return { writes, reads }
}
const show = (children: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{children}</ClientProvider>)
it("keeps denied access distinct and does not fetch Cloud data", async () => {
  denied = true
  const { reads } = fixture()
  show(<CloudsWorkspace />)
  expect(screen.getByText("Cloud access is restricted")).toBeInTheDocument()
  expect(reads.some((r) => r.endsWith("/clouds"))).toBe(false)
})
it("shows a Cloud spread over several accounts of one provider", async () => {
  fixture({ spread: true })
  show(<CloudsWorkspace />)
  expect(
    await screen.findByText("3 targets across 3 accounts on 2 providers")
  ).toBeInTheDocument()
  expect(screen.getByText("Production DO")).toBeInTheDocument()
  expect(screen.getByText("EU DO")).toBeInTheDocument()
  expect(screen.getByText("2 accounts")).toBeInTheDocument()
  expect(screen.getByText("ams3")).toBeInTheDocument()
})
it("distinguishes filtered targets, clears the filter and discards old project/environment scope", async () => {
  const user = userEvent.setup()
  const { reads } = fixture()
  show(<CloudsWorkspace />)
  await screen.findByText("Frontend")
  await user.type(
    screen.getByRole("textbox", { name: "Filter targets" }),
    "missing"
  )
  expect(screen.getByText("No matching targets")).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Clear filter" }))
  expect(screen.getByText("Frontend")).toBeInTheDocument()
  await user.click(screen.getByRole("combobox", { name: "Environment" }))
  await user.click(await screen.findByRole("option", { name: /staging/ }))
  await waitFor(() =>
    expect(reads.some((r) => r.includes("/environments/stage/cloud"))).toBe(
      true
    )
  )
  await user.click(screen.getByRole("combobox", { name: "Project" }))
  await user.click(await screen.findByRole("option", { name: /Other/ }))
  await waitFor(() =>
    expect(reads.some((r) => r.includes("/projects/other/clouds"))).toBe(true)
  )
  expect(screen.getByRole("textbox", { name: "Filter targets" })).toHaveValue(
    ""
  )
})
it("creates a Cloud and refreshes the generated list", async () => {
  const user = userEvent.setup()
  const { writes } = fixture({ noClouds: true })
  show(<CloudsWorkspace />)
  await screen.findByText("Create your first Cloud")
  await user.click(screen.getAllByRole("button", { name: "New Cloud" })[0])
  await user.type(
    screen.getByRole("textbox", { name: "Cloud name" }),
    "Customer Cloud"
  )
  await user.click(screen.getByRole("button", { name: "Save Cloud" }))
  await waitFor(() =>
    expect(screen.getByRole("combobox", { name: "Cloud" })).toHaveTextContent(
      "Customer Cloud"
    )
  )
  expect(writes[0].body).toEqual({ name: "Customer Cloud" })
})
it("shows read errors without presenting an empty Cloud result", async () => {
  fixture({ failure: "list" })
  show(<CloudsWorkspace />)
  await screen.findByText("Cloud read failed.")
  expect(screen.queryByText("Create your first Cloud")).not.toBeInTheDocument()
})
it("blocks a stale preview until refreshed", async () => {
  const user = userEvent.setup()
  const { writes } = fixture({ failure: "Cloud configuration changed" })
  show(
    <ChangeReview
      projectId="p"
      title="Switch frontend"
      change={{ cloudId: "cloud", targetId: "target", revision: 1 }}
      onClose={() => undefined}
      onApplied={() => undefined}
    />
  )
  await user.click(
    await screen.findByRole("button", { name: "Switch and sync" })
  )
  await screen.findByText(
    "This review is out of date. Refresh it before applying."
  )
  expect(
    screen.queryByRole("button", { name: "Switch and sync" })
  ).not.toBeInTheDocument()
  expect(writes.find((w) => w.url.endsWith("/apply"))?.body).toMatchObject({
    preview_id: "preview",
    revision: "digest",
  })
  await user.click(screen.getByRole("button", { name: "Refresh preview" }))
  expect(
    await screen.findByRole("button", { name: "Switch and sync" })
  ).toBeEnabled()
})
it("reports each environment's partial result and sends retry through the persisted sync", async () => {
  const user = userEvent.setup()
  const writes: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.method === "POST") {
        writes.push(url)
        return Response.json({ id: "sync", status: "pending" })
      }
      return Response.json({
        syncs: [
          {
            id: "sync",
            status: "failed",
            attempt: 1,
            environments: [
              env,
              {
                ...env,
                environment_id: "stage",
                environment: "staging",
                status: "succeeded",
                error: "",
              },
            ],
          },
        ],
      })
    }
  )
  show(
    <SyncHistory
      projectId="p"
      environmentId="prod"
      refresh={0}
      onChanged={async () => undefined}
    />
  )
  await screen.findByText("Readiness failed")
  expect(screen.getByText("staging")).toBeInTheDocument()
  expect(
    within(screen.getByRole("article")).getByText("In sync")
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Retry sync" }))
  await waitFor(() =>
    expect(writes).toEqual(["/api/v1/projects/p/cloud-syncs/sync/retry"])
  )
})

it("keeps immutable review placements distinct when both snapshots share a target ID", async () => {
  fixture()
  show(
    <ChangeReview
      projectId="p"
      title="Switch frontend"
      change={{ cloudId: "cloud", targetId: "target", revision: 1 }}
      onClose={() => undefined}
      onApplied={() => undefined}
    />
  )
  await screen.findByText("Current: vercel / Account A")
  expect(screen.getByText("Desired: vercel / b")).toBeInTheDocument()
})

it("reuses the same acceptance key after a lost response", async () => {
  const user = userEvent.setup()
  const accepted: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith("/connections"))
        return Response.json({ connections: [] })
      if (url.endsWith("/preview")) return Response.json(preview)
      const payload = JSON.parse(String(init?.body))
      accepted.push(payload)
      return accepted.length === 1
        ? Response.json({ message: "Response was lost" }, { status: 503 })
        : Response.json({ id: "sync", status: "pending" })
    }
  )
  const applied = vi.fn()
  show(
    <ChangeReview
      projectId="p"
      title="Switch frontend"
      change={{ cloudId: "cloud", targetId: "target", revision: 1 }}
      onClose={() => undefined}
      onApplied={applied}
    />
  )
  await user.click(
    await screen.findByRole("button", { name: "Switch and sync" })
  )
  await screen.findByText(/Response was lost/)
  await user.click(screen.getByRole("button", { name: "Switch and sync" }))
  await waitFor(() => expect(applied).toHaveBeenCalledOnce())
  expect(accepted).toHaveLength(2)
  expect(accepted[0]).toEqual(accepted[1])
  expect(accepted[0].idempotency_key).toBeTruthy()
})

it("saves an adopted service through the optimistic generated manifest API", async () => {
  const user = userEvent.setup()
  const writes: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        writes.push(JSON.parse(String(init.body)))
        return Response.json({ id: "next", revision: 4, manifest: {} })
      }
      return Response.json({
        id: "saved",
        revision: 3,
        manifest: {
          version: 1,
          defaults: { provider: "vercel", region: "iad1" },
          services: { web: { source: "apps/web" }, api: { source: "cmd/api" } },
          resources: { db: { kind: "postgres" } },
        },
      })
    }
  )
  const { ServiceAdoption } =
    await import("@/components/cloud-targets/service-adoption")
  const environments = [
    { id: "prod", name: "production" },
    { id: "stage", name: "staging" },
  ] as import("@/client/forge/loomplane/types").Environment[]
  const targets = [
    { id: "target", name: "frontend", displayName: "Frontend" },
  ] as import("@/client/forge/loomplane/types").Target[]
  show(
    <ServiceAdoption
      projectId="p"
      environment={environments[0]}
      environments={environments}
      targets={targets}
      onSaved={async () => undefined}
    />
  )
  await user.click(
    await screen.findByRole("combobox", { name: "Cloud target for web" })
  )
  await user.click(await screen.findByRole("option", { name: /Frontend/ }))
  await user.click(
    screen.getAllByRole("button", { name: "Review service edit" })[0]
  )
  expect(screen.getByLabelText("Manifest adoption preview")).toHaveTextContent(
    '"cloud_target": "frontend"'
  )
  await user.click(screen.getByRole("button", { name: "Save service target" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatchObject({
    base_revision: 3,
    manifest: {
      services: {
        web: {
          environments: {
            production: { cloud_target: "frontend" },
            staging: { provider: "vercel", region: "iad1" },
          },
        },
        api: { provider: "vercel" },
      },
      resources: { db: { provider: "vercel" } },
    },
  })
})

it("requires routing evidence and sends it for the selected environment", async () => {
  const user = userEvent.setup()
  const writes: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/connections"))
        return Response.json({ connections: [] })
      if (init?.method === "POST") {
        writes.push(JSON.parse(String(init.body)))
        return Response.json({ id: "sync", status: "pending" })
      }
      return Response.json({
        syncs: [
          {
            id: "sync",
            status: "routing_pending",
            attempt: 1,
            environments: [
              {
                ...env,
                status: "succeeded",
                error: "",
                external_routing: true,
              },
            ],
          },
        ],
      })
    }
  )
  show(
    <SyncHistory
      projectId="p"
      environmentId="prod"
      refresh={0}
      onChanged={async () => undefined}
    />
  )
  const confirm = await screen.findByRole("button", { name: "Confirm routing" })
  expect(confirm).toBeDisabled()
  expect(
    screen.getByText("Deployment ready, routing pending")
  ).toBeInTheDocument()
  await user.type(
    screen.getByRole("textbox", { name: "Routing evidence for production" }),
    "DNS checked"
  )
  await user.click(confirm)
  await waitFor(() =>
    expect(writes).toEqual([
      { environment_id: "prod", evidence: "DNS checked" },
    ])
  )
})

it.each(["empty", "environments", "list"])(
  "keeps project selection available when selected scope is %s",
  async (state) => {
    const user = userEvent.setup()
    const { reads } = fixture({
      noEnvironments: state === "empty",
      failure: state === "empty" ? undefined : state,
    })
    show(<CloudsWorkspace />)
    if (state === "empty") await screen.findByText("No environments yet")
    else if (state === "environments")
      await screen.findByText("Environment read failed.")
    else await screen.findByText("Cloud read failed.")
    await user.click(screen.getByRole("combobox", { name: "Project" }))
    await user.click(await screen.findByRole("option", { name: /Other/ }))
    await waitFor(() =>
      expect(
        reads.some((url) => url.includes("/projects/other/environments"))
      ).toBe(true)
    )
    expect(screen.getByRole("combobox", { name: "Project" })).toHaveTextContent(
      "Other project"
    )
    await screen.findByText("Frontend")
  }
)

it("keeps blocked previews distinct from environments without deployed services", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    String(input).endsWith("/clouds")
      ? Response.json({ clouds: [] })
      : String(input).endsWith("/connections")
        ? Response.json({ connections: [] })
        : Response.json({
            ...preview,
            blockers: ["The proposed Cloud is missing target frontend."],
            environments: [
              { ...env, services: [], error: "", status: "blocked" },
            ],
          })
  )
  show(
    <ChangeReview
      projectId="p"
      title="Review assignment"
      assignment={{ environmentId: "prod", cloudId: "empty", revision: 1 }}
      onClose={() => undefined}
      onApplied={() => undefined}
    />
  )
  await screen.findByText("The proposed Cloud is missing target frontend.")
  expect(screen.getByRole("button", { name: "Switch and sync" })).toBeDisabled()
  expect(screen.queryByText("No deployed services")).not.toBeInTheDocument()
  expect(
    screen.getByText(
      "Service details are unavailable while this preview is blocked."
    )
  ).toBeInTheDocument()
})

it.each([false, true])(
  "shows revision-bound Cloud assignment account changes, removal=%s",
  async (remove) => {
    vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith("/connections"))
        return Response.json({
          connections: [
            { id: "a", name: "Old account" },
            { id: "b", name: "New account" },
          ],
        })
      if (url.endsWith("/clouds"))
        return Response.json({
          clouds: [
            { id: "old", name: "Old Cloud" },
            { id: "new", name: "New Cloud" },
          ],
        })
      return Response.json({
        ...preview,
        before: undefined,
        after: undefined,
        input: { cloud_id: remove ? "" : "new", revision: 1 },
        before_assignment: {
          cloud_id: "old",
          cloud_revision: 2,
          targets: [{ ...target, connection_id: "a" }],
        },
        ...(remove
          ? {}
          : {
              after_assignment: {
                cloud_id: "new",
                cloud_revision: 3,
                targets: [
                  { ...target, connection_id: "b", provider: "cloud-run" },
                ],
              },
            }),
      })
    })
    show(
      <ChangeReview
        projectId="p"
        title="Review assignment"
        assignment={{
          environmentId: "prod",
          cloudId: remove ? "" : "new",
          revision: 1,
        }}
        onClose={() => undefined}
        onApplied={() => undefined}
      />
    )
    await screen.findByText("Current Cloud: Old Cloud (revision 2)")
    expect(
      screen.getByText("Current: vercel / Old account")
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        remove
          ? "Desired Cloud: Direct placement"
          : "Desired Cloud: New Cloud (revision 3)"
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        remove
          ? "Desired: Target removed from this environment"
          : "Desired: cloud-run / New account"
      )
    ).toBeInTheDocument()
  }
)

it("explains retained preview protection when deleting an unassigned Cloud", async () => {
  const user = userEvent.setup()
  const { writes } = fixture({ failure: "delete" })
  show(<CloudsWorkspace />)
  await user.click(await screen.findByRole("button", { name: "Manage Cloud" }))
  await user.click(screen.getByRole("button", { name: "Delete Cloud" }))
  await user.click(screen.getByRole("button", { name: "Confirm delete Cloud" }))
  await screen.findByText(
    "This Cloud is protected while environments, deployments, syncs or recent previews reference it. Preview references expire after 24 hours."
  )
  expect(writes[0].url).toBe("/api/v1/projects/p/clouds/cloud")
  expect(screen.getByRole("textbox", { name: "Cloud name" })).toHaveValue(
    "Production"
  )
})
