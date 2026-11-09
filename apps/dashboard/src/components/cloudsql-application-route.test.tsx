import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { PlacementPanel } from "@/components/deployment-targets"
import type { Environment, Manifest } from "@/client/forge/loomplane/types"

const permissions = vi.hoisted(() => ({ update: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => permissions.update, session: {} }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.update = true
})
const route = {
  consumer_project: "app-project",
  network: "projects/app-project/global/networks/apps",
  subnetwork: "projects/app-project/regions/us-central1/subnetworks/apps",
}
function mount({ denied = false, inherited = false } = {}) {
  const writes: Record<string, unknown>[] = []
  const manifest: Manifest = {
    version: 1,
    services: {
      api: {
        source: "go:cmd/api",
        provider: "cloud-run",
        region: "us-central1",
        ...(inherited ? { cloud_sql_network: route } : {}),
      },
    },
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      let body: unknown = {}
      if (url.endsWith("/graph"))
        body = {
          graph: {
            sources: [],
            services: [
              {
                id: "svc:go:cmd/api",
                entrypoint: "cmd/api",
                path: ".",
                kind: "http-service",
                deployable: true,
              },
            ],
          },
        }
      if (url.endsWith("/providers"))
        body = {
          providers: [
            {
              name: "cloud-run",
              title: "Cloud Run",
              cloud: "gcp",
              available: true,
              regions: ["us-central1", "europe-west1"],
              runs: [{ workload: "http-service" }],
            },
          ],
        }
      if (url.endsWith("/connections")) body = { connections: [] }
      if (init?.body) {
        writes.push(JSON.parse(String(init.body)))
        if (denied)
          return Response.json(
            { message: "You cannot edit this project" },
            { status: 403 }
          )
        body = {
          id: "rev_2",
          project_id: "project",
          revision: 2,
          yaml: "version: 1",
          manifest,
        }
      }
      return Response.json(body)
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <PlacementPanel
        projectId="project"
        manifest={manifest}
        revision={1}
        row={{
          section: "services",
          name: "api",
          kind: "http-service",
          entry: manifest.services!.api,
        }}
        environment="production"
        environments={[{ id: "env", name: "production" } as Environment]}
        mode="save"
        onClose={() => {}}
      />
    </ClientProvider>
  )
  return writes
}
// The route sits in the panel's "Build and network" section.
async function openRoute(user = userEvent.setup()) {
  await user.click(
    await screen.findByRole("button", { name: "Build and network" })
  )
  return user
}
async function enterRoute() {
  const user = await openRoute()
  const network = await screen.findByRole("textbox", {
    name: "PSC network for api",
  })
  await user.click(network)
  await user.paste(route.network)
  await user.click(screen.getByRole("textbox", { name: "PSC subnet for api" }))
  await user.paste(route.subnetwork)
  return user
}
it("saves the canonical application route through placement review without deploying", async () => {
  const writes = mount()
  const user = await enterRoute()
  await user.click(screen.getByRole("button", { name: "Save placement" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        placement: {
          section: "services",
          name: "api",
          environment: "production",
          cloud_sql_network: route,
        },
      },
    ])
  )
})
it("refuses a partial or wrong-region route before saving", async () => {
  const writes = mount()
  const user = await openRoute()
  await user.click(
    await screen.findByRole("textbox", { name: "PSC network for api" })
  )
  await user.paste(route.network)
  expect(screen.getByRole("button", { name: "Save placement" })).toBeDisabled()
  await user.click(screen.getByRole("textbox", { name: "PSC subnet for api" }))
  await user.paste(route.subnetwork.replace("us-central1", "europe-west1"))
  expect(screen.getByRole("button", { name: "Save placement" })).toBeDisabled()
  expect(writes).toEqual([])
})
it("shows inherited routes and restores inheritance with an explicit empty edit", async () => {
  const writes = mount({ inherited: true })
  const user = await openRoute()
  expect(
    await screen.findByRole("textbox", { name: "PSC network for api" })
  ).toHaveValue(route.network)
  await user.clear(screen.getByRole("textbox", { name: "PSC network for api" }))
  await user.clear(screen.getByRole("textbox", { name: "PSC subnet for api" }))
  await user.click(screen.getByRole("button", { name: "Save placement" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        placement: {
          section: "services",
          name: "api",
          environment: "production",
          cloud_sql_network: {
            consumer_project: "",
            network: "",
            subnetwork: "",
          },
        },
      },
    ])
  )
})
it("retains the route after denial so the user can retry", async () => {
  mount({ denied: true })
  const user = await enterRoute()
  await user.click(screen.getByRole("button", { name: "Save placement" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "You cannot edit this project"
  )
  expect(
    screen.getByRole("textbox", { name: "PSC network for api" })
  ).toHaveValue(route.network)
  expect(screen.getByRole("button", { name: "Save placement" })).toBeEnabled()
})
it("keeps the route read-only without project update permission", async () => {
  permissions.update = false
  mount({ inherited: true })
  await openRoute()
  expect(
    await screen.findByRole("textbox", { name: "PSC network for api" })
  ).toBeDisabled()
  expect(
    screen.getByRole("textbox", { name: "PSC subnet for api" })
  ).toBeDisabled()
})
