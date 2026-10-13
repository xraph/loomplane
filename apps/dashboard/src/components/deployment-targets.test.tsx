import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { DeployTab, ManifestEditor } from "@/components/deploy"
import { DeployDialog } from "@/components/deploy-dialog"
import {
  DeploymentTargets,
  DeploymentAccounts,
} from "@/components/deployment-targets"
import type { Manifest, Environment } from "@/client/forge/loomplane/types"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
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

it("keeps unsaved YAML when a placement revision arrives and prevents overwriting it", async () => {
  const project = {
    id: "p",
  } as import("@/client/forge/loomplane/types").Project
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

it("shows failed account setup reads instead of silently hiding infrastructure choices", async () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ error: "Target catalog unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      })
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentAccounts projectId="p" environment="preview" />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("alert", {}, { timeout: 5000 })
  ).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
})

it("waits for a saved placement before allowing a plan", async () => {
  let finishSave: (response: Response) => void = () => {}
  const pending = new Promise<Response>((resolve) => {
    finishSave = resolve
  })
  const manifest: Manifest = {
    version: 1,
    services: { worker: { source: "go:cmd/worker", provider: "render" } },
  }
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    let body: unknown = { connections: [] }
    if (url.endsWith("/manifest"))
      body = { id: "r1", revision: 1, yaml: "version: 1\n", manifest }
    if (url.endsWith("/graph"))
      body = {
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
      }
    if (url.endsWith("/providers"))
      body = {
        providers: [
          {
            name: "render",
            title: "Render",
            cloud: "render",
            available: true,
            runs: [{ workload: "worker" }],
          },
        ],
      }
    if (url.endsWith("/sources") || url.endsWith("/deployed"))
      body = { sources: [] }
    if (url.endsWith("/plans")) body = { plans: [] }
    if (url.endsWith("/deployments")) body = { deployments: [] }
    if (url.endsWith("/projects"))
      body = { projects: [{ id: "p", name: "Example" }] }
    if (url.endsWith("/environments"))
      body = { environments: [{ id: "e", name: "preview" }] }
    if (url.endsWith("/manifest/edits")) return pending
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    })
  })
  const view = (open: boolean) => (
    <ClientProvider client={forgeClient}>
      <>
        <DeployTab
          project={
            { id: "p" } as import("@/client/forge/loomplane/types").Project
          }
          environments={[{ id: "e", name: "preview" } as Environment]}
        />
        <DeployDialog
          preset={open ? { project: "p", environment: "preview" } : null}
          onOpenChange={() => {}}
        />
      </>
    </ClientProvider>
  )
  const { rerender } = render(view(false))
  await userEvent.click(
    await screen.findByRole("switch", { name: "Enable worker in preview" })
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Save worker placement" })
  )
  const blocked = screen
    .getByRole("button", { name: /^Plan$/ })
    .hasAttribute("disabled")
  rerender(view(true))
  const dialogBlocked = (
    await screen.findByRole("button", { name: "Plan deployment" })
  ).hasAttribute("disabled")
  finishSave(
    new Response(JSON.stringify({ id: "r2", revision: 2 }), {
      headers: { "Content-Type": "application/json" },
    })
  )
  expect(blocked).toBe(true)
  expect(dialogBlocked).toBe(true)
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
      let body: unknown = {}
      if (url.endsWith("/graph"))
        body = {
          project_id: "p",
          graph: {
            version: 1,
            services: [
              {
                id: "svc:go:cmd/worker",
                ecosystem: "go",
                entrypoint: "cmd/worker",
                path: ".",
                kind: "worker",
                deployable: true,
              },
            ],
            sources: [],
          },
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
        }
      if (url.endsWith("/connections")) body = { connections: [] }
      if (url.endsWith("/manifest/edits")) {
        edit = JSON.parse(String(init?.body))
        body = { id: "r2", revision: 2 }
      }
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentTargets
        projectId="p"
        manifest={m}
        environments={[{ id: "e", name: "preview" } as Environment]}
        environment="preview"
        onEnvironment={() => {}}
      />
    </ClientProvider>
  )
  const target = await screen.findByRole("combobox", {
    name: "Target for worker",
  })
  await userEvent.click(target)
  const list = await screen.findByRole("listbox")
  expect(
    within(list).queryByRole("option", { name: "Vercel" })
  ).not.toBeInTheDocument()
  await userEvent.click(within(list).getByRole("option", { name: "AWS ECS" }))
  await userEvent.type(screen.getByLabelText("Region for worker"), "us-east-1")
  await userEvent.click(
    screen.getByRole("button", { name: "Save worker placement" })
  )
  expect(
    await screen.findByText(
      "Placement saved. Create a new plan to review the change."
    )
  ).toBeInTheDocument()
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
