import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"

import { AddServiceDialog } from "./add-service-dialog"

const navigate = vi.fn()
vi.mock("@/lib/router", async (original) => ({
  ...(await original<typeof import("@/lib/router")>()),
  useNavigate: () => navigate,
}))
vi.mock("@/lib/project-permissions", () => ({
  useProjectCan: () => () => true,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const yaml = "version: 1\nservices:\n  api:\n    source: cmd/api\n"
const manifest = {
  version: 1,
  services: { api: { source: "cmd/api" } },
  resources: { db: { kind: "postgres" } },
}

type Write = { path: string; body: Record<string, unknown> }

let writes: Write[] = []
beforeEach(() => {
  writes = []
  navigate.mockReset()
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        if (path.endsWith("/manifest/edits") && body.dry_run) {
          const add = (body.add_service ?? body.add_resource) as {
            name: string
          }
          return Response.json({
            revision: 2,
            yaml: `${yaml}  ${add.name}:\n    source: added\n`,
          })
        }
        writes.push({ path, body })
        if (path.endsWith("/plans"))
          return Response.json({ id: "plan_1" }, { status: 201 })
        return Response.json({ revision: 2, yaml, manifest }, { status: 201 })
      }
      switch (path) {
        case "/api/v1/projects/p/manifest":
          return Response.json({ revision: 1, yaml, manifest })
        case "/api/v1/projects/p/manifest/suggestions":
          return Response.json({
            project_id: "p",
            analyzed: true,
            services: [
              {
                id: "svc:go:cmd/worker",
                kind: "worker",
                ecosystem: "go",
                path: "cmd/worker",
                service: {
                  name: "worker",
                  source: "go:cmd/worker",
                  provider: "ecs-fargate",
                  needs: ["db"],
                },
              },
            ],
          })
        case "/api/v1/catalog/kinds":
          return Response.json({
            kinds: [
              {
                name: "sample",
                title: "Sample gateway",
                placement: "app-project",
                targets: ["server"],
                versions: ["1"],
                engine: false,
              },
            ],
          })
        case "/api/v1/connections":
          return Response.json({
            connections: [
              {
                id: "conn_1",
                cloud: "server",
                name: "box",
                status: "ready",
                details: { architecture: "amd64" },
              },
            ],
          })
        case "/api/v1/providers":
          return Response.json({
            providers: [
              {
                name: "ecs-fargate",
                cloud: "aws",
                title: "ECS on Fargate",
                available: true,
                runs: [{ workload: "worker" }],
              },
            ],
          })
        case "/api/v1/projects/p/sources":
          return Response.json({ sources: [] })
        case "/api/v1/catalog/endpoints":
          return Response.json({ endpoints: [] })
      }
      return Response.json({})
    }
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const project = { id: "p", name: "Shop", repositories: ["acme/shop"] } as never
const environments = [
  { id: "e1", name: "production" },
  { id: "e2", name: "staging" },
] as never

function show(start?: "code" | "catalog" | "source") {
  return render(
    <ClientProvider client={forgeClient}>
      <AddServiceDialog
        project={project}
        environments={environments}
        environment="staging"
        start={start}
        open
        onOpenChange={() => {}}
      />
    </ClientProvider>
  )
}

it("adds a service analysis found and plans the environment it opened on", async () => {
  const user = userEvent.setup()
  show()
  const found = await screen.findByRole("list", {
    name: "Services analysis found",
  })
  await user.click(within(found).getByRole("button", { name: /worker/ }))
  expect(screen.getByLabelText("Name")).toHaveValue("worker")
  expect(screen.getByRole("checkbox", { name: /db/ })).toBeChecked()
  // The server's own YAML for the add, before anything is saved.
  const changes = await screen.findByRole("region", {
    name: "Changes to loomplane.yaml",
  })
  await waitFor(() => expect(changes).toHaveTextContent("+ worker:"))
  await user.click(
    await screen.findByRole("button", { name: "Add and plan staging" })
  )
  await waitFor(() =>
    expect(writes).toEqual([
      {
        path: "/api/v1/projects/p/manifest/edits",
        body: {
          add_service: {
            name: "worker",
            source: "go:cmd/worker",
            provider: "ecs-fargate",
            needs: ["db"],
          },
        },
      },
      {
        path: "/api/v1/projects/p/plans",
        body: { environment: "staging" },
      },
    ])
  )
  expect(navigate).toHaveBeenCalledWith({
    to: "/deployments/$id",
    params: { id: "plan_1" },
  })
})

it("adds a catalog kind on the only target, wired to the services that use it", async () => {
  const user = userEvent.setup()
  show("catalog")
  await user.click(
    await screen.findByRole("button", { name: /Sample gateway/ })
  )
  expect(screen.getByRole("radio", { name: /box/ })).toHaveAttribute(
    "aria-checked",
    "true"
  )
  expect(screen.getByLabelText("Name")).toHaveValue("sample")
  await user.click(screen.getByRole("checkbox", { name: /api/ }))

  // Only in staging would leave api in production without it.
  await user.click(screen.getByRole("radio", { name: "Only staging" }))
  expect(
    screen.getByText(/api runs in production and would need sample/)
  ).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Add and plan staging" })
  ).toBeDisabled()

  await user.click(screen.getByRole("radio", { name: "Every environment" }))
  await user.click(screen.getByRole("button", { name: "Add only" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        path: "/api/v1/projects/p/manifest/edits",
        body: {
          add_resource: {
            name: "sample",
            kind: "sample",
            provider: "server",
            target: "conn_1",
            version: "1",
            needed_by: ["api"],
          },
        },
      },
    ])
  )
  expect(navigate).not.toHaveBeenCalled()
})

it("names a service from its path, and won't take a name in use", async () => {
  const user = userEvent.setup()
  show("source")
  await user.type(await screen.findByLabelText("Source"), "services/billing")
  expect(screen.getByLabelText("Name")).toHaveValue("billing")
  const name = screen.getByLabelText("Name")
  await user.clear(name)
  await user.type(name, "api")
  expect(
    screen.getByText("The manifest already has something called api.")
  ).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Add and plan staging" })
  ).toBeDisabled()
})

it("adds a service on a target of another Cloud instead of a provider", async () => {
  const user = userEvent.setup()
  const base = globalThis.fetch
  const target = (id: string, name: string, cloud: string) => ({
    id,
    name,
    display_name: name.toUpperCase(),
    cloud_id: cloud,
    provider: "ecs-fargate",
    connection_id: "conn",
    revision: 1,
  })
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path === "/api/v1/projects/p/default-cloud")
        return Response.json({
          project_id: "p",
          revision: 1,
          cloud: {
            cloud_id: "main",
            name: "Main",
            default: true,
            targets: [target("t1", "eu-run", "main")],
          },
        })
      if (path === "/api/v1/projects/p/clouds")
        return Response.json({
          clouds: [
            { id: "main", name: "Main" },
            { id: "lab", name: "GPU Lab" },
          ],
        })
      if (path === "/api/v1/projects/p/clouds/lab/targets")
        return Response.json({ targets: [target("t2", "a100", "lab")] })
      return base(input, init)
    }
  )
  show()
  const found = await screen.findByRole("list", {
    name: "Services analysis found",
  })
  await user.click(within(found).getByRole("button", { name: /worker/ }))
  await user.click(screen.getByRole("combobox", { name: "Runs on" }))
  const lab = await screen.findByRole("group", { name: "GPU Lab" })
  expect(screen.getByRole("group", { name: "Main" })).toHaveTextContent(
    "EU-RUN"
  )
  await user.click(within(lab).getByRole("option", { name: "A100" }))
  expect(screen.getByRole("combobox", { name: "Runs on" })).toHaveTextContent(
    "a100 in GPU Lab"
  )
  await user.click(
    await screen.findByRole("button", { name: "Add and plan staging" })
  )
  await waitFor(() => expect(writes.length).toBeGreaterThan(0))
  expect(writes[0].body).toEqual({
    add_service: {
      name: "worker",
      source: "go:cmd/worker",
      cloud: "GPU Lab",
      cloud_target: "a100",
      needs: ["db"],
    },
  })
})
