import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ResourceReviewDialog } from "./review-dialog"

const navigate = vi.fn()
vi.mock("@/lib/router", () => ({ useNavigate: () => navigate }))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
test("creation options come from the installed registry and checked accounts", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("projects"))
      return Response.json({ projects: [{ id: "p", name: "Orders" }] })
    if (path.endsWith("environments"))
      return Response.json({ environments: [{ name: "production" }] })
    if (path.endsWith("manifest"))
      return Response.json({
        manifest: { version: 1, services: { api: { source: "." } } },
      })
    throw new Error(path)
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceReviewDialog
        open
        onOpenChange={() => {}}
        providers={[
          {
            name: "future",
            title: "Future Provider",
            cloud: "future",
            available: true,
            provisions: [{ kind: "future-db", creates: "Future database" }],
            regions: ["west"],
          } as never,
        ]}
        connections={[
          {
            id: "c",
            name: "Checked future account",
            cloud: "future",
            status: "ready",
          } as never,
        ]}
      />
    </ClientProvider>
  )
  expect(await screen.findByText("Future database")).toBeInTheDocument()
  expect(screen.getByRole("combobox", { name: "Account" })).toHaveTextContent(
    "Checked future account"
  )
  await userEvent.click(screen.getByRole("combobox", { name: "Resource kind" }))
  expect(
    await screen.findByRole("option", { name: "future-db" })
  ).toBeInTheDocument()
})

test("required provider settings gate review and are saved in the manifest", async () => {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        writes.push({ path, body: JSON.parse(String(init.body)) })
        return Response.json(
          path.endsWith("edits") ? { revision: 2 } : { id: "plan-1" }
        )
      }
      if (path.endsWith("projects"))
        return Response.json({ projects: [{ id: "p", name: "Orders" }] })
      if (path.endsWith("environments"))
        return Response.json({ environments: [{ name: "production" }] })
      if (path.endsWith("manifest"))
        return Response.json({ manifest: { version: 1 } })
      throw new Error(path)
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ResourceReviewDialog
        open
        onOpenChange={() => {}}
        providers={[
          {
            name: "vercel",
            title: "Vercel",
            cloud: "vercel",
            available: true,
            provisions: [
              {
                kind: "vercel-blob",
                creates: "Blob store",
                settings: [
                  {
                    name: "project_id",
                    title: "Existing Vercel project ID",
                    required: true,
                    pattern: "^prj_[a-z0-9]+$",
                  },
                  {
                    name: "environment",
                    title: "Environment scope",
                    required: true,
                    pattern: "^(production|preview)$",
                    default: "production",
                  },
                ],
              },
            ],
          } as never,
        ]}
        connections={[
          { id: "c", name: "Team", cloud: "vercel", status: "ready" } as never,
        ]}
      />
    </ClientProvider>
  )
  const project = await screen.findByLabelText("Existing Vercel project ID")
  const review = screen.getByRole("button", { name: "Review deployment plan" })
  expect(review).toBeDisabled()
  expect(screen.getByLabelText("Environment scope")).toHaveValue("production")
  await userEvent.type(project, "not-a-project")
  expect(review).toBeDisabled()
  await userEvent.clear(project)
  await userEvent.type(project, "prj_existing")
  expect(review).toBeEnabled()
  await userEvent.click(review)
  await vi.waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0].body).toEqual({
    add_resource: {
      name: "resource",
      kind: "vercel-blob",
      provider: "vercel",
      target: "c",
      region: "",
      version: "",
      enabled_in: "production",
      settings: { project_id: "prj_existing", environment: "production" },
    },
  })
  expect(writes[1].path).toBe("/api/v1/projects/p/plans")
})

let services: Record<string, unknown> = {}
let edits: Record<string, unknown>[] = []
function stubManifest() {
  edits = []
  services = { api: {}, web: {}, worker: {} }
  navigate.mockReset()
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        if (path.endsWith("/manifest/edits")) {
          edits.push(body)
          return Response.json({ revision: 2 }, { status: 201 })
        }
        return Response.json({ id: "plan_1" }, { status: 201 })
      }
      switch (path) {
        case "/api/v1/projects":
          return Response.json({ projects: [{ id: "p", name: "Shop" }] })
        case "/api/v1/projects/p/environments":
          return Response.json({
            environments: [{ id: "e1", name: "production" }],
          })
        case "/api/v1/projects/p/manifest":
          return Response.json({
            revision: 1,
            yaml: "version: 1\n",
            manifest: { version: 1, services },
          })
      }
      return Response.json({})
    }
  )
}

const providers = [
  {
    name: "digitalocean",
    cloud: "digitalocean",
    title: "DigitalOcean",
    available: true,
    regions: ["nyc"],
    provisions: [
      { kind: "postgres", creates: "Managed PostgreSQL", versions: ["17"] },
    ],
  },
] as never
const connections = [
  { id: "conn_do", cloud: "digitalocean", name: "Team", status: "ready" },
] as never

function show() {
  return render(
    <ClientProvider client={forgeClient}>
      <ResourceReviewDialog
        open
        onOpenChange={() => {}}
        offer={{ kind: "postgres", provider: "digitalocean" }}
        providers={providers}
        connections={connections}
      />
    </ClientProvider>
  )
}

test("wires every service you tick in the edit that adds the resource", async () => {
  stubManifest()
  const user = userEvent.setup()
  show()
  const api = await screen.findByRole("checkbox", { name: "api" })
  expect(api).not.toBeChecked()
  await user.click(screen.getByRole("checkbox", { name: "worker" }))
  await user.click(api)
  expect(
    screen.getByText("api and worker will need resource in your manifest.")
  ).toBeInTheDocument()
  await user.click(
    screen.getByRole("button", { name: "Review deployment plan" })
  )
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan_1" },
    })
  )
  expect(edits).toHaveLength(1)
  expect(edits[0].add_resource).toMatchObject({
    name: "resource",
    kind: "postgres",
    needed_by: ["api", "worker"],
  })
})

test("ticks the only service, and leaves it out when you clear it", async () => {
  stubManifest()
  services = { api: {} }
  const user = userEvent.setup()
  show()
  const api = await screen.findByRole("checkbox", { name: "api" })
  expect(api).toBeChecked()
  await user.click(api)
  await user.click(
    screen.getByRole("button", { name: "Review deployment plan" })
  )
  await waitFor(() => expect(edits).toHaveLength(1))
  expect(edits[0].add_resource).not.toHaveProperty("needed_by")
})
