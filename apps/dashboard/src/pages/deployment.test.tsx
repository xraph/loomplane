import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DeploymentPage } from "@/pages/deployment"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => undefined,
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const plan = (release?: object) => ({
  id: "plan_1",
  project_id: "proj_1",
  environment: "production",
  status: "waiting",
  executable: true,
  actions: [],
  connections: {},
  catalog: false,
  warnings: [],
  problems: [],
  sources: [],
  revision: "r1",
  summary: { adopt: 0, create: 0, delete: 0, keep: 0, replace: 0, update: 1 },
  created_at: "2026-10-26T09:00:00Z",
  ...(release ? { release } : {}),
})

function serve(p: object) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost").pathname
    const body = url.endsWith("/plans/plan_1")
      ? p
      : url.endsWith("/projects/proj_1")
        ? { id: "proj_1", name: "Shop", slug: "shop" }
        : {}
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    })
  })
  render(
    <ClientProvider client={forgeClient}>
      <DeploymentPage id="plan_1" />
    </ClientProvider>
  )
}

test("a plan without a release shows no canary panel", async () => {
  serve(plan())
  expect(
    await screen.findByRole("region", { name: "Steps" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("heading", { name: "Canary" })
  ).not.toBeInTheDocument()
})

test("a plan with a canary release shows the canary panel", async () => {
  serve(
    plan({
      strategy: "canary",
      steps: [5, 100],
      hold_seconds: 600,
      canary: ["svc-web"],
      thresholds: {},
    })
  )
  expect(
    await screen.findByRole("heading", { name: "Canary" })
  ).toBeInTheDocument()
})
