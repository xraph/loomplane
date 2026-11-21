import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import type { Plan } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { PlanRepairs } from "./plan-repairs"

vi.mock("@/lib/router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => (
    <span>{children}</span>
  ),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
it("repairs both builds, a port and a disabled need in one revision before replanning", async () => {
  const replan = vi.fn()
  const write = vi.fn()
  const problems = [
    ["api", "build-isolation-required", "build.builder"],
    ["worker", "build-isolation-required", "build.builder"],
    ["api", "host-port-required", "endpoint.host_port"],
    ["api", "need-off", "needs"],
  ].map(([name, code, field]) => ({
    name,
    code,
    path: `services.${name}.${field}`,
    subject: "service",
  }))
  const plan = {
    projectId: "p",
    manifestRevision: 8,
    environment: "production",
    problems,
    actions: [
      {
        name: "api",
        subject: "service",
        op: "create",
        service: { buildTarget: { builder: "builder" } },
      },
      { name: "postgres", subject: "resource", op: "create" },
    ],
  } as Plan
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        write(JSON.parse(String(init.body)))
        return Response.json({ revision: 9 })
      }
      if (String(input).endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: "shared",
              name: "Target server",
              cloud: "server",
              status: "ready",
              details: {},
            },
            {
              id: "builder",
              name: "Build node",
              cloud: "server",
              status: "ready",
              details: { build_workers: "dedicated" },
            },
            {
              id: "registry",
              name: "Images",
              cloud: "registry",
              status: "ready",
            },
          ],
        })
      return Response.json({
        revision: 8,
        manifest: {
          version: 1,
          services: {
            api: {
              cloud_target: "backend",
              build: { context: "web" },
              needs: ["resource", "redis"],
              environments: { preview: { enabled: false } },
            },
            worker: { cloud_target: "backend" },
          },
          resources: {
            resource: { environments: { production: { enabled: false } } },
            postgres: { kind: "postgres" },
            redis: { kind: "redis" },
          },
        },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <PlanRepairs plan={plan} replan={replan} onClose={() => {}} />
    </ClientProvider>
  )
  const user = userEvent.setup()
  for (const [label, name] of [
    ["Dedicated builder", "Build node"],
    ["Image registry", "Images"],
    ["Replace resource for api", "postgres"],
  ]) {
    await user.click(await screen.findByRole("combobox", { name: label }))
    expect(screen.queryByRole("option", { name: "Target server" })).toBeNull()
    await user.click(await screen.findByRole("option", { name }))
  }
  await user.type(screen.getByRole("spinbutton"), "8080")
  await user.click(screen.getByRole("button", { name: "Save and plan again" }))
  await waitFor(() => expect(replan).toHaveBeenCalledOnce())
  expect(write).toHaveBeenCalledOnce()
  expect(write.mock.calls[0]![0]).toMatchObject({
    base_revision: 8,
    manifest: {
      services: {
        api: {
          cloud_target: "backend",
          needs: ["postgres", "redis"],
          environments: {
            production: {
              build: {
                context: "web",
                builder: "builder",
                registry: "registry",
              },
              endpoint: { host_port: 8080 },
            },
            preview: { enabled: false },
          },
        },
        worker: {
          cloud_target: "backend",
          environments: {
            production: { build: { builder: "builder", registry: "registry" } },
          },
        },
      },
    },
  })
})
