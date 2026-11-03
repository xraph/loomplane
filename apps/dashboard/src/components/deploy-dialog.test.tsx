import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DeploymentForm } from "@/components/deploy-dialog"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: {} }),
}))

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  navigate.mockReset()
  forgeClient.clear()
})

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const provider = {
  name: "vercel",
  title: "Vercel",
  cloud: "vercel",
  available: true,
  runs: [{ workload: "static-site" }],
}

const graphService = (name: string, source = "frontend") => ({
  id: `svc:${source}:node:apps/${name}`,
  name,
  source,
  path: `apps/${name}`,
  kind: "static-site",
  deployable: true,
})

/**
 * project stubs one project's API: three services from two repositories,
 * with web and worker running the latest commit in preview and api behind.
 */
function project({
  writes = [],
  excluded = [],
  plan,
}: {
  writes?: { method: string; url: string; body: unknown }[]
  excluded?: unknown[]
  plan?: Promise<void>
} = {}) {
  let revision = 1
  const manifest = {
    version: 1,
    services: {
      api: { repo: "frontend", source: "apps/api", provider: "vercel" },
      web: { repo: "frontend", source: "apps/web", provider: "vercel" },
      worker: { repo: "backend", source: "apps/worker", provider: "vercel" },
    },
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = init?.method ?? "GET"
      if (init?.body)
        writes.push({ method, url, body: JSON.parse(String(init.body)) })
      if (url.endsWith("/projects"))
        return json({ projects: [{ id: "p", name: "Shop" }] })
      if (url.endsWith("/environments"))
        return json({ environments: [{ id: "e", name: "preview" }] })
      if (url.endsWith("/manifest/edits") || method === "PUT") {
        revision++
        return json({ id: `r${revision}`, revision, yaml: "", manifest })
      }
      if (url.endsWith("/manifest"))
        return json({
          id: `r${revision}`,
          revision,
          yaml: "version: 1\n",
          manifest,
        })
      if (url.endsWith("/graph"))
        return json({
          project_id: "p",
          excluded,
          graph: {
            sources: [
              { alias: "frontend", commit_sha: "new1" },
              { alias: "backend", commit_sha: "new2" },
            ],
            // The graph leaves out what the project excludes.
            services: [
              graphService("api"),
              graphService("web"),
              ...(excluded.length ? [] : [graphService("worker", "backend")]),
            ],
          },
        })
      if (url.includes("/bindings"))
        return json({
          bindings: [
            { subject: "service", name: "api", commit_sha: "old1" },
            { subject: "service", name: "web", commit_sha: "new1" },
            { subject: "service", name: "worker", commit_sha: "new2" },
          ],
        })
      if (url.endsWith("/providers")) return json({ providers: [provider] })
      if (url.endsWith("/sources"))
        return json({
          sources: [
            {
              id: "s1",
              alias: "frontend",
              repository: "https://github.com/acme/web",
            },
            {
              id: "s2",
              alias: "backend",
              repository: "https://github.com/acme/api",
            },
          ],
        })
      if (url.endsWith("/deployed"))
        return json({ sources: [{ alias: "frontend" }, { alias: "backend" }] })
      if (url.endsWith("/plans") && method === "POST") {
        await plan
        return json({ id: "plan_one", status: "planning" })
      }
      if (url.endsWith("/plans")) return json({ plans: [] })
      if (url.endsWith("/deployments")) return json({ deployments: [] })
      if (url.endsWith("/connections")) return json({ connections: [] })
      return json({})
    }
  )
  return writes
}

function mount(preset: Record<string, string> = { project: "p" }) {
  const done = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentForm preset={preset} done={done} />
    </ClientProvider>
  )
  return done
}

it("shows a failed project read and recovers on retry", async () => {
  let failed = true
  vi.stubGlobal("fetch", async () =>
    failed
      ? json({ error: "Projects unavailable" }, 503)
      : json({ projects: [] })
  )
  mount({})
  expect(
    await screen.findByRole("alert", {}, { timeout: 5000 })
  ).toBeInTheDocument()
  failed = false
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("No projects to deploy")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})

it("ticks what changed, holds a repository and plans every listed service", async () => {
  let finish: () => void = () => {}
  const writes = project({ plan: new Promise<void>((r) => (finish = r)) })
  const done = mount({ project: "p", environment: "preview" })
  const api = await screen.findByRole("checkbox", { name: "Deploy api" })
  await vi.waitFor(() => expect(api).toBeChecked())
  expect(screen.getByRole("checkbox", { name: "Deploy web" })).not.toBeChecked()
  expect(screen.getByText("New commits")).toBeInTheDocument()
  expect(screen.getAllByText("No changes")).toHaveLength(2)
  await userEvent.click(screen.getByRole("button", { name: "All" }))
  expect(
    screen.getByRole("region", { name: "Deploy summary" })
  ).toHaveTextContent("3 services to preview")
  await userEvent.click(
    within(
      screen.getByRole("radiogroup", { name: "What backend deploys" })
    ).getByRole("radio", { name: "Hold" })
  )
  const review = screen.getByRole("button", { name: "Review plan" })
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  expect(review).toBeDisabled()
  finish()
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan_one" },
    })
  )
  // Every listed service is picked, so the plan names none of them.
  expect(writes).toEqual([
    {
      method: "POST",
      url: expect.stringContaining("/projects/p/plans"),
      body: { environment: "preview", scope: ["frontend"] },
    },
  ])
  expect(done).not.toHaveBeenCalled()
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(done).toHaveBeenCalled()
})

it("saves pending placement as one revision, then plans only the picked services", async () => {
  const writes = project()
  mount({ project: "p", environment: "preview", service: "api" })
  const api = await screen.findByRole("checkbox", { name: "Deploy api" })
  await vi.waitFor(() => expect(api).toBeChecked())
  expect(
    screen.getByRole("checkbox", { name: "Deploy worker" })
  ).not.toBeChecked()

  for (const name of ["api", "web"]) {
    await userEvent.click(
      screen.getByRole("button", { name: `Change where ${name} runs` })
    )
    expect(
      await screen.findByText(
        "Becomes preview's standing setting when you review the plan. Nothing saves before that."
      )
    ).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole("switch", { name: `Enable ${name} in preview` })
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Use this placement" })
    )
  }
  // A service whose placement changes goes out with the deploy.
  expect(screen.getByRole("checkbox", { name: "Deploy web" })).toBeChecked()
  expect(screen.getAllByText("Moves to Off in preview")).toHaveLength(2)
  expect(
    screen.getByRole("region", { name: "Deploy summary" })
  ).toHaveTextContent("2 services to preview · 2 placement changes")
  expect(writes).toEqual([])

  await userEvent.click(screen.getByRole("button", { name: "Review plan" }))
  await vi.waitFor(() => expect(navigate).toHaveBeenCalled())
  expect(writes).toHaveLength(2)
  expect(writes[0]).toMatchObject({
    method: "PUT",
    url: expect.stringContaining("/projects/p/manifest"),
    body: {
      base_revision: 1,
      manifest: {
        services: {
          api: { environments: { preview: { enabled: false } } },
          web: { environments: { preview: { enabled: false } } },
          worker: { provider: "vercel" },
        },
      },
    },
  })
  expect(writes[0].body).not.toHaveProperty(
    "manifest.services.worker.environments"
  )
  expect(writes[1]).toEqual({
    method: "POST",
    url: expect.stringContaining("/plans"),
    body: { environment: "preview", services: ["api", "web"] },
  })
})

it("needs a picked service before it plans", async () => {
  project()
  mount({ project: "p", environment: "preview" })
  const api = await screen.findByRole("checkbox", { name: "Deploy api" })
  await vi.waitFor(() => expect(api).toBeChecked())
  await userEvent.click(api)
  expect(screen.getByRole("button", { name: "Review plan" })).toBeDisabled()
  expect(
    screen.getByText("Pick at least one service to deploy.")
  ).toBeInTheDocument()
})

it("never lists a service the project excludes", async () => {
  project({
    excluded: [
      {
        rule: { kind: "service", match: "svc:backend:node:apps/worker" },
        service: graphService("worker", "backend"),
      },
    ],
  })
  mount({ project: "p", environment: "preview" })
  expect(
    await screen.findByRole("checkbox", { name: "Deploy api" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("checkbox", { name: "Deploy worker" })
  ).not.toBeInTheDocument()
  expect(
    screen.getByText(/1 service is excluded from Shop and never deploys/)
  ).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Manage exclusions" })).toBeVisible()
})

it("holds the plan while loomplane.yaml has unsaved changes", async () => {
  project()
  mount({ project: "p", environment: "preview" })
  const review = await screen.findByRole("button", { name: "Review plan" })
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(
    screen.getByRole("button", { name: "More deploy options" })
  )
  await userEvent.click(
    await screen.findByRole("menuitem", { name: "View loomplane.yaml" })
  )
  await userEvent.type(
    screen.getByRole("textbox", { name: "Manifest" }),
    "# Unsaved"
  )
  expect(review).toBeDisabled()
  expect(
    screen.getByText("Save or undo your loomplane.yaml changes to continue.")
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Undo changes" }))
  expect(review).toBeEnabled()
})

it("starts a first deployment from the analyzed draft without overwriting a newer manifest", async () => {
  const writes: unknown[] = []
  let saved = false
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith("/projects"))
        return json({ projects: [{ id: "p", name: "Web" }] })
      if (url.endsWith("/environments"))
        return json({ environments: [{ id: "e", name: "preview" }] })
      if (url.endsWith("/sources") || url.endsWith("/deployed"))
        return json({ sources: [] })
      if (url.endsWith("/manifest/draft"))
        return json({
          yaml: "version: 1\nservices: {}\n",
          manifest: { version: 1, services: {} },
          sources: [],
        })
      if (url.endsWith("/manifest") && init?.method === "PUT") {
        writes.push(JSON.parse(String(init.body)))
        saved = true
        return json({
          id: "r1",
          revision: 1,
          manifest: { version: 1, services: {} },
          yaml: "version: 1\nservices: {}\n",
        })
      }
      if (url.endsWith("/manifest") && !saved)
        return json({ error: "No manifest" }, 404)
      return json({})
    }
  )
  mount({ project: "p" })
  expect(
    await screen.findByRole("button", { name: "Review plan" })
  ).toBeDisabled()
  await userEvent.click(
    await screen.findByRole("button", { name: "Use suggested configuration" })
  )
  expect(writes).toEqual([
    { yaml: "version: 1\nservices: {}\n", base_revision: 0 },
  ])
})

it("does not deploy a different project when an explicit project preset is unavailable", async () => {
  vi.stubGlobal("fetch", async () =>
    json({
      projects: [{ id: "other", name: "Another project" }],
      environments: [],
      sources: [],
    })
  )
  mount({ project: "removed" })
  expect(await screen.findByText("Project unavailable")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Review plan" })
  ).not.toBeInTheDocument()
})

it("does not silently change an unavailable environment preset", async () => {
  vi.stubGlobal("fetch", async () =>
    json({
      projects: [{ id: "p", name: "Web" }],
      environments: [{ id: "e", name: "preview" }],
      sources: [],
    })
  )
  mount({ project: "p", environment: "production" })
  expect(await screen.findByText("Environment unavailable")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Review plan" })
  ).not.toBeInTheDocument()
})
