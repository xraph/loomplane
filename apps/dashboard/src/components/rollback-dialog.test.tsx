import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { RollbackDialog } from "@/components/rollback-dialog"

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
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

const hour = 3_600_000

const target = () => ({
  environment: "production",
  target: {
    deployment_id: "dep_2",
    plan_id: "plan_2",
    kind: "release",
    status: "succeeded",
    finished_at: new Date(Date.now() - 10 * 60_000).toISOString(),
  },
  restore: {
    deployment_id: "dep_1",
    plan_id: "plan_1",
    kind: "release",
    status: "succeeded",
    finished_at: new Date(Date.now() - 2 * hour - 60_000).toISOString(),
  },
  commits: [
    { alias: "web", from: "bbbbbbb1234567", to: "aaaaaaa1234567" },
    { alias: "api", from: "ccccccc1234567", to: "ccccccc1234567" },
  ],
})

type Reply = { status?: number; body: unknown }

function serve(reply: Reply = { body: target() }) {
  const calls: { url: string; method: string; body?: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const u = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      calls.push({
        url: u.pathname + u.search,
        method,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      const r: Reply = u.pathname.endsWith("/rollbacks")
        ? { status: 201, body: { id: "plan_rb", status: "planning" } }
        : reply
      return new Response(JSON.stringify(r.body), {
        status: r.status ?? 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <RollbackDialog
        open
        onOpenChange={() => undefined}
        projectId="p"
        environment="production"
        deploymentId="dep_2"
      />
    </ClientProvider>
  )
  return calls
}

it("shows the release it restores and needs a reason", async () => {
  const calls = serve()
  expect(
    await screen.findByText("Roll production back to the release from 2h ago")
  ).toBeInTheDocument()
  expect(calls[0]?.url).toBe(
    "/api/v1/projects/p/environments/production/rollback-target?deployment_id=dep_2"
  )
  const submit = screen.getByRole("button", { name: "Plan the rollback" })
  expect(submit).toBeDisabled()
  await userEvent.type(screen.getByRole("textbox", { name: "Why" }), "  ")
  expect(submit).toBeDisabled()
  await userEvent.type(
    screen.getByRole("textbox", { name: "Why" }),
    "Checkout errors doubled"
  )
  expect(submit).toBeEnabled()
})

it("creates the rollback plan and opens it", async () => {
  const calls = serve()
  await screen.findByText("Roll production back to the release from 2h ago")
  await userEvent.type(
    screen.getByRole("textbox", { name: "Why" }),
    "Checkout errors doubled"
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Plan the rollback" })
  )
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan_rb" },
    })
  )
  expect(calls.filter((c) => c.method === "POST")).toEqual([
    {
      url: "/api/v1/projects/p/rollbacks",
      method: "POST",
      body: {
        environment: "production",
        reason: "Checkout errors doubled",
        deployment_id: "dep_2",
      },
    },
  ])
})

it("says why it can't roll back", async () => {
  serve({
    status: 409,
    body: {
      message:
        "a deployment is running in this environment; wait for it to finish, or cancel it, before rolling back",
    },
  })
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "wait for it to finish, or cancel it, before rolling back"
  )
  expect(
    screen.getByRole("button", { name: "Plan the rollback" })
  ).toBeDisabled()
})
