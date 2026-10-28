import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"

import { ServiceScaling } from "./service-scaling"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function fixture(api: Record<string, unknown>) {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        writes.push({ path, body: JSON.parse(String(init.body)) })
        return Response.json({ revision: 2 }, { status: 201 })
      }
      return Response.json({
        revision: 1,
        yaml: "",
        manifest: { version: 1, services: { api } },
      })
    }
  )
  return writes
}

const project = { id: "p", name: "Shop" } as never
const environments = [
  { id: "e1", name: "production" },
  { id: "e2", name: "staging" },
] as never
const show = (provider: string) =>
  render(
    <ClientProvider client={forgeClient}>
      <ServiceScaling
        project={project}
        name="api"
        environments={environments}
        deployed={{ production: { provider } as never }}
      />
    </ClientProvider>
  )

it("saves a range for an autoscaling provider to the environment", async () => {
  const user = userEvent.setup()
  const writes = fixture({ scale: { min: 1, max: 2 } })
  show("cloud-run")
  expect(
    await screen.findByText(/Cloud Run adds instances/)
  ).toBeInTheDocument()
  const most = await screen.findByRole("spinbutton", { name: "Most instances" })
  await user.clear(most)
  await user.type(most, "4")
  await user.type(screen.getByRole("spinbutton", { name: "vCPU each" }), "1")
  await user.type(
    screen.getByRole("spinbutton", { name: "Memory each, MiB" }),
    "1024"
  )
  await user.click(screen.getByRole("button", { name: "Save to manifest" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        path: "/api/v1/projects/p/manifest/edits",
        body: {
          placement: {
            section: "services",
            name: "api",
            environment: "production",
            scale: { min: 1, max: 4 },
            size: { cpu: 1, memory: 1024 },
          },
        },
      },
    ])
  )
  expect(
    screen.getByText(/take effect at the next deployment/)
  ).toBeInTheDocument()
})

it("asks a fixed-count provider for one number, and saves it as min and max", async () => {
  const user = userEvent.setup()
  const writes = fixture({ scale: { min: 2, max: 2 } })
  show("digitalocean")
  const count = await screen.findByRole("spinbutton", { name: "Instances" })
  expect(
    screen.queryByRole("spinbutton", { name: "Most instances" })
  ).not.toBeInTheDocument()
  await user.clear(count)
  await user.type(count, "3")
  await user.click(screen.getByRole("button", { name: "Save to manifest" }))
  await waitFor(() =>
    expect(writes[0]?.body).toMatchObject({
      placement: { scale: { min: 3, max: 3 } },
    })
  )
})

it("says a server runs one instance and offers nothing to change", async () => {
  fixture({})
  show("server")
  expect(
    await screen.findByText(/A server runs one container/)
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Save to manifest" })
  ).not.toBeInTheDocument()
})

it("leaves a GPU size to the manifest so the form can't drop it", async () => {
  fixture({
    size: { cpu: 4, memory: 16384, gpu: { kind: "nvidia-l4", count: 1 } },
  })
  show("kubernetes")
  expect(
    await screen.findByText(/asks for GPUs or shared memory/)
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("spinbutton", { name: "vCPU each" })
  ).not.toBeInTheDocument()
})
