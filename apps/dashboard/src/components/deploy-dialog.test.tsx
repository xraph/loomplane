import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DeployDialog, DeploymentForm } from "@/components/deploy-dialog"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
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

it("shows a failed project read and recovers on retry", async () => {
  let failed = true
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(
        JSON.stringify(
          failed ? { error: "Projects unavailable" } : { projects: [] }
        ),
        {
          status: failed ? 503 : 200,
          headers: { "Content-Type": "application/json" },
        }
      )
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeployDialog onOpenChange={() => {}} preset={{}} />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("alert", {}, { timeout: 5000 })
  ).toBeInTheDocument()
  failed = false
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("No projects to deploy")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})

it("configures a service before creating a plan from its saved manifest", async () => {
  const writes: { url: string; body: unknown }[] = []
  let finishPlanning: () => void = () => {}
  const planning = new Promise<void>((resolve) => {
    finishPlanning = resolve
  })
  let enabled = true
  let revision = 1
  const manifest = () => ({
    version: 1,
    services: {
      web: {
        source: "apps/web",
        provider: "vercel",
        environments: { preview: { enabled } },
      },
    },
  })
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.body) writes.push({ url, body: JSON.parse(String(init.body)) })
      let body: unknown = {}
      if (url.endsWith("/projects"))
        body = { projects: [{ id: "p", name: "Web" }] }
      if (url.endsWith("/environments"))
        body = { environments: [{ id: "e", name: "preview" }] }
      if (url.endsWith("/manifest/edits")) {
        const edit = JSON.parse(String(init?.body))
        enabled = edit.placement.enabled
        revision++
        body = {
          id: `r${revision}`,
          revision,
          yaml: "version: 1",
          manifest: manifest(),
        }
      } else if (url.endsWith("/manifest"))
        body = {
          id: `r${revision}`,
          revision,
          yaml: "version: 1",
          manifest: manifest(),
        }
      if (url.endsWith("/providers"))
        body = {
          providers: [
            {
              name: "vercel",
              title: "Vercel",
              cloud: "vercel",
              available: true,
              runs: [{ workload: "static-site" }],
            },
          ],
        }
      if (url.endsWith("/graph"))
        body = {
          graph: {
            services: [
              {
                id: "svc:node:apps/web",
                path: "apps/web",
                kind: "static-site",
                deployable: true,
              },
            ],
            sources: [],
          },
        }
      if (url.endsWith("/connections")) body = { connections: [] }
      if (url.endsWith("/sources") || url.endsWith("/deployed"))
        body = { sources: [{ alias: "frontend" }, { alias: "backend" }] }
      if (url.endsWith("/plans") && init?.method === "POST") await planning
      if (url.endsWith("/plans"))
        body =
          init?.method === "POST"
            ? { id: "plan_one", status: "planning" }
            : { plans: [] }
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  const cancel = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentForm
        page
        preset={{ project: "p", environment: "preview" }}
        done={cancel}
      />
    </ClientProvider>
  )
  await userEvent.click(
    await screen.findByRole("switch", { name: "Enable web in preview" })
  )
  expect(
    screen.getByRole("button", { name: "Review deployment" })
  ).toBeDisabled()
  expect(screen.getByRole("combobox", { name: "Project" })).toBeDisabled()
  await userEvent.click(
    screen.getByRole("button", { name: "Save web placement" })
  )
  const review = screen.getByRole("button", { name: "Review deployment" })
  await vi.waitFor(() => expect(review).toBeEnabled())
  expect(
    screen.getByRole("complementary", { name: "Deployment summary" })
  ).toHaveTextContent(/Services included\s*0/)
  await userEvent.click(screen.getByRole("button", { name: "View manifest" }))
  await userEvent.type(
    screen.getByRole("textbox", { name: "Manifest" }),
    "# Unsaved configuration"
  )
  expect(review).toBeDisabled()
  await userEvent.click(screen.getByRole("button", { name: "Hide manifest" }))
  expect(review).toBeDisabled()
  await userEvent.click(screen.getByRole("button", { name: "View manifest" }))
  expect(screen.getByRole("textbox", { name: "Manifest" })).toHaveValue(
    "version: 1# Unsaved configuration"
  )
  await userEvent.click(screen.getByRole("button", { name: "Undo changes" }))
  expect(review).toBeEnabled()
  await userEvent.click(screen.getByRole("button", { name: "backend" }))
  await userEvent.click(review)
  const projectLocked = screen
    .getByRole("combobox", { name: "Project" })
    .hasAttribute("disabled")
  finishPlanning()
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan_one" },
    })
  )
  expect(projectLocked).toBe(true)
  expect(writes).toContainEqual({
    url: expect.stringContaining("/manifest/edits"),
    body: {
      placement: {
        section: "services",
        name: "web",
        environment: "preview",
        enabled: false,
      },
    },
  })
  expect(cancel).not.toHaveBeenCalled()
  expect(navigate).toHaveBeenCalledWith({
    to: "/deployments/$id",
    params: { id: "plan_one" },
  })
  expect(writes).toContainEqual({
    url: expect.stringContaining("/plans"),
    body: { environment: "preview", scope: ["frontend"] },
  })
})

it("starts a first deployment from the analyzed draft without overwriting a newer manifest", async () => {
  const writes: unknown[] = []
  let saved = false
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      let body: unknown = {}
      let status = 200
      if (url.endsWith("/projects"))
        body = { projects: [{ id: "p", name: "Web" }] }
      if (url.endsWith("/environments"))
        body = { environments: [{ id: "e", name: "preview" }] }
      if (url.endsWith("/sources") || url.endsWith("/deployed"))
        body = { sources: [] }
      if (url.endsWith("/manifest") && init?.method !== "PUT" && !saved) {
        status = 404
        body = { error: "No manifest" }
      }
      if (url.endsWith("/manifest/draft"))
        body = {
          yaml: "version: 1\nservices: {}\n",
          manifest: { version: 1, services: {} },
          sources: [],
        }
      if (url.endsWith("/manifest") && init?.method === "PUT") {
        writes.push(JSON.parse(String(init.body)))
        saved = true
        body = {
          id: "r1",
          revision: 1,
          manifest: { version: 1, services: {} },
          yaml: "version: 1\nservices: {}\n",
        }
      }
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeployDialog preset={{ project: "p" }} onOpenChange={() => {}} />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("button", { name: "Review deployment" })
  ).toBeDisabled()
  await userEvent.click(
    await screen.findByRole("button", { name: "Use suggested configuration" })
  )
  expect(writes).toEqual([
    { yaml: "version: 1\nservices: {}\n", base_revision: 0 },
  ])
})

it("does not deploy a different project when an explicit project preset is unavailable", async () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(
        JSON.stringify({
          projects: [{ id: "other", name: "Another project" }],
          environments: [],
          sources: [],
        }),
        { headers: { "Content-Type": "application/json" } }
      )
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentForm page preset={{ project: "removed" }} done={() => {}} />
    </ClientProvider>
  )
  expect(await screen.findByText("Project unavailable")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Review deployment" })
  ).not.toBeInTheDocument()
})

it("does not silently change an unavailable environment preset", async () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(
        JSON.stringify({
          projects: [{ id: "p", name: "Web" }],
          environments: [{ id: "e", name: "preview" }],
          sources: [],
        }),
        { headers: { "Content-Type": "application/json" } }
      )
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentForm
        page
        preset={{ project: "p", environment: "production" }}
        done={() => {}}
      />
    </ClientProvider>
  )
  expect(await screen.findByText("Environment unavailable")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Review deployment" })
  ).not.toBeInTheDocument()
})
