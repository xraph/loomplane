import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
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

const plan = (release?: object, status = "ready") => ({
  id: "plan_1",
  project_id: "proj_1",
  environment: "production",
  status,
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
  created_by: "usr_1",
  ...(release ? { release } : {}),
})

function serve(p: object, more: Record<string, unknown> = {}) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost").pathname
    const extra = Object.entries(more).find(([suffix]) => url.endsWith(suffix))
    const body = extra
      ? extra[1]
      : url.endsWith("/plans/plan_1")
        ? p
        : url.endsWith("/projects/proj_1")
          ? { id: "proj_1", name: "Shop", slug: "shop" }
          : {}
    if (extra && typeof extra[1] === "number")
      return new Response(JSON.stringify({ message: "refused" }), {
        status: extra[1],
        headers: { "Content-Type": "application/json" },
      })
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

const canaried = {
  strategy: "canary",
  steps: [5, 100],
  hold_seconds: 600,
  canary: ["svc-web"],
  thresholds: {},
}

test.each([
  ["a blocked plan", plan(canaried, "blocked")],
  ["a rejected plan", plan(canaried, "rejected")],
  ["a release with no canaried service", plan({ ...canaried, canary: [] })],
])("%s shows no canary panel", async (_, p) => {
  serve(p)
  expect(
    await screen.findByRole("region", { name: "Steps" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("heading", { name: "Canary" })
  ).not.toBeInTheDocument()
})

test("a promotion shows its source, how long it soaked and its images", async () => {
  serve(
    {
      ...plan(),
      kind: "promotion",
      from_environment: "staging",
      artifacts: [
        { service: "web", reused: true, from: "staging", revision: "" },
        {
          service: "api",
          reused: false,
          reason: "staging's registry is in another account",
        },
      ],
    },
    {
      "/plans/plan_1/soak": {
        environment: "staging",
        running: true,
        since: "2026-10-29T09:00:00Z",
        duration: 3 * 3600 * 1e9,
        healthy: true,
        unverified: false,
        reasons: [],
        services: [],
      },
    }
  )
  expect(await screen.findByText("Promoted from staging")).toBeInTheDocument()
  expect(await screen.findByText("Reuses staging's image")).toBeInTheDocument()
  expect(
    screen.getByText("Builds again: staging's registry is in another account")
  ).toBeInTheDocument()
  expect(
    await screen.findByText("staging has run these commits for 3h 00m, healthy")
  ).toBeInTheDocument()
  expect(screen.queryByText("Unverified")).not.toBeInTheDocument()
})

test("a promotion whose soak lacks evidence is marked unverified", async () => {
  serve(
    { ...plan(), kind: "promotion", from_environment: "staging" },
    {
      "/plans/plan_1/soak": {
        environment: "staging",
        running: true,
        since: "2026-10-29T11:00:00Z",
        duration: 40 * 60 * 1e9,
        healthy: false,
        unverified: true,
        reasons: ["web: 30 requests, fewer than the 200 a verdict needs."],
        services: [],
      },
    }
  )
  expect(
    await screen.findByText(
      "staging has run these commits for 40m 00s, not healthy: web: 30 requests, fewer than the 200 a verdict needs."
    )
  ).toBeInTheDocument()
  expect(screen.getByText("Unverified")).toBeInTheDocument()
})

test("an automatic rollback names the automation and lists its leftovers", async () => {
  serve({
    ...plan(),
    kind: "rollback",
    created_by: "loomplane:org_1",
    rollback: {
      reason:
        "api: 4.1% of the candidate's requests failed; the ceiling is 1%.",
      restore_deployment_id: "dep_1",
      restore_finished_at: "2026-10-29T08:00:00Z",
      restore_plan_id: "plan_0",
      target_deployment_id: "dep_2",
      target_plan_id: "plan_2",
      trigger: "watch",
    },
    leftovers: [
      {
        name: "orders-db",
        subject: "postgres",
        op: "delete",
        data_bearing: true,
        reason: "It holds data, so the rollback keeps it.",
      },
      {
        name: "cache",
        subject: "redis",
        op: "update",
        data_bearing: false,
        reason: "",
      },
    ],
  })
  expect(await screen.findByText("Automatic rollback")).toBeInTheDocument()
  expect(screen.getAllByText("Loomplane automation").length).toBeGreaterThan(0)
  const leftovers = await screen.findByRole("list", { name: "Leftovers" })
  const db = within(leftovers).getByText("orders-db").closest("li")!
  expect(within(db).getByText("Data")).toBeInTheDocument()
  const cache = within(leftovers).getByText("cache").closest("li")!
  expect(within(cache).queryByText("Data")).not.toBeInTheDocument()
})

const finished = (id: string, startedAt: string) => ({
  id,
  org_id: "org_1",
  project_id: "proj_1",
  plan_id: id === "dep_1" ? "plan_1" : "plan_9",
  environment: "production",
  environment_id: "env_1",
  status: "succeeded",
  steps: [],
  sources: [],
  commit_sha: "",
  error: "",
  retries: 0,
  started_at: startedAt,
  started_by: "usr_1",
  finished_at: startedAt,
})

const target = {
  environment: "production",
  target: {
    deployment_id: "dep_1",
    plan_id: "plan_1",
    kind: "release",
    status: "succeeded",
  },
  restore: {
    deployment_id: "dep_0",
    plan_id: "plan_0",
    kind: "release",
    status: "succeeded",
  },
  commits: [],
}

test("the newest finished deployment offers a rollback", async () => {
  serve(
    { ...plan(undefined, "approved"), environment_id: "env_1" },
    {
      "/plans/plan_1/deployment": finished("dep_1", "2026-10-29T10:00:00Z"),
      "/projects/proj_1/deployments": {
        deployments: [finished("dep_1", "2026-10-29T10:00:00Z")],
      },
      "/environments/production/rollback-target": target,
      "/deployments/dep_1/watch": 404,
    }
  )
  expect(
    await screen.findByRole("button", { name: "Roll back" })
  ).toBeInTheDocument()
})

test("an older deployment offers no rollback", async () => {
  serve(
    { ...plan(undefined, "approved"), environment_id: "env_1" },
    {
      "/plans/plan_1/deployment": finished("dep_1", "2026-10-29T10:00:00Z"),
      "/projects/proj_1/deployments": {
        deployments: [
          finished("dep_9", "2026-10-29T11:00:00Z"),
          finished("dep_1", "2026-10-29T10:00:00Z"),
        ],
      },
      // The server refuses: dep_1 isn't the newest release.
      "/environments/production/rollback-target": 422,
      "/deployments/dep_1/watch": 404,
    }
  )
  expect((await screen.findAllByText("dep_1")).length).toBeGreaterThan(0)
  await new Promise((r) => setTimeout(r, 50))
  expect(
    screen.queryByRole("button", { name: "Roll back" })
  ).not.toBeInTheDocument()
})

// A Cloud sync or catalog deployment after it isn't a release, so the
// server still rolls dep_1 back, and the page offers it.
test("a newer Cloud sync deployment doesn't hide the rollback", async () => {
  serve(
    { ...plan(undefined, "approved"), environment_id: "env_1" },
    {
      "/plans/plan_1/deployment": finished("dep_1", "2026-10-29T10:00:00Z"),
      "/projects/proj_1/deployments": {
        deployments: [
          finished("dep_sync", "2026-10-29T11:00:00Z"),
          finished("dep_1", "2026-10-29T10:00:00Z"),
        ],
      },
      "/environments/production/rollback-target": target,
      "/deployments/dep_1/watch": 404,
    }
  )
  expect(
    await screen.findByRole("button", { name: "Roll back" })
  ).toBeInTheDocument()
})
