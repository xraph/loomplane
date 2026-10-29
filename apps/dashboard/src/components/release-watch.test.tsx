import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { hhmm, ReleaseWatchPanel } from "@/components/release-watch"

vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
  }: {
    children: React.ReactNode
    to: string
    params?: { id: string }
  }) => <a href={to.replace("$id", params?.id ?? "")}>{children}</a>,
  useNavigate: () => () => undefined,
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const thresholds = {
  error_rate: 0.01,
  error_rate_delta: 0.01,
  min_requests: 200,
  p95_latency_ratio: 1.5,
}

// 14:15 in whatever zone the tests run in: the panel prints local time.
const endsAt = new Date(2026, 9, 29, 14, 15).toISOString()

const watch = (over: object) => ({
  id: "rw_1",
  org_id: "org_1",
  project_id: "p",
  environment_id: "env_p",
  environment: "production",
  deployment_id: "dep_1",
  plan_id: "plan_1",
  status: "watching",
  verdict: "",
  reason: "",
  thresholds,
  services: [
    {
      name: "api",
      provider: "cloudrun",
      binding_id: "bnd_1",
      op: "update",
      revision: "api-00042",
      verdict: "pass",
      reason: "",
      checked_at: "2026-10-29T14:01:00Z",
    },
  ],
  unwatched: [
    {
      name: "web",
      provider: "vercel",
      reason: "Vercel exposes no error counts",
    },
  ],
  held: [],
  held_released: false,
  unverified: true,
  rollback_plan_id: "",
  rollback_approved_at: null,
  starts_at: new Date(2026, 9, 29, 14, 0).toISOString(),
  ends_at: endsAt,
  bound_at: new Date(2026, 9, 29, 14, 0).toISOString(),
  checked_at: null,
  settled_at: null,
  created_at: "2026-10-29T14:00:00Z",
  updated_at: "2026-10-29T14:01:00Z",
  ...over,
})

function serve(body: object, status = 200) {
  const fetch = vi.fn(
    async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      })
  )
  vi.stubGlobal("fetch", fetch)
  render(
    <ClientProvider client={forgeClient}>
      <ReleaseWatchPanel deploymentId="dep_1" />
    </ClientProvider>
  )
  return fetch
}

it("shows a running watch with each service's verdict", async () => {
  serve(watch({}))
  const until = `Watching until ${hhmm(Date.parse(endsAt))}`
  expect(until).toBe("Watching until 14:15")
  expect(await screen.findByText(until)).toBeInTheDocument()
  const services = screen.getByRole("list", { name: "Watched services" })
  const api = within(services).getByText("api").closest("li")!
  expect(api).toHaveTextContent("Passing")
  const web = within(services).getByText("web").closest("li")!
  expect(web).toHaveTextContent("Not watched: Vercel exposes no error counts")
  expect(screen.getByText("Unverified")).toBeInTheDocument()
})

it("marks an inconclusive release unverified", async () => {
  serve(
    watch({
      status: "inconclusive",
      verdict: "inconclusive",
      reason: "api: 120 requests, fewer than the 200 a verdict needs.",
      unwatched: [],
      unverified: true,
      settled_at: "2026-10-29T14:30:00Z",
    })
  )
  expect(await screen.findByText("Unverified")).toBeInTheDocument()
  expect(
    screen.getByText(
      "Loomplane couldn't confirm this release was healthy, so nothing was rolled back.",
      { exact: false }
    )
  ).toBeInTheDocument()
})

it("shows a breach and the rollback it started", async () => {
  serve(
    watch({
      status: "rolled_back",
      verdict: "fail",
      reason:
        "api: 4.1% of the candidate's requests failed; the ceiling is 1%.",
      unwatched: [],
      unverified: false,
      rollback_plan_id: "plan_rb",
      rollback_approved_at: "2026-10-29T14:06:00Z",
      settled_at: "2026-10-29T14:12:00Z",
    })
  )
  expect(await screen.findByText("Rolled back")).toBeInTheDocument()
  expect(
    screen.getByText(
      "api: 4.1% of the candidate's requests failed; the ceiling is 1%.",
      { exact: false }
    )
  ).toBeInTheDocument()
  expect(
    screen.getByText("Loomplane automation", { exact: false })
  ).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Open the rollback" })
  ).toHaveAttribute("href", "/deployments/plan_rb")
})

it("says a superseded release wasn't judged to the end", async () => {
  serve(
    watch({
      status: "superseded",
      unwatched: [],
      unverified: true,
      settled_at: "2026-10-29T14:10:00Z",
    })
  )
  expect(await screen.findByText("Superseded")).toBeInTheDocument()
  expect(
    screen.getByText(
      "A newer release replaced this one before the watch finished, so it wasn't judged to the end."
    )
  ).toBeInTheDocument()
  expect(screen.getByText("Unverified")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})

it("raises a rollback that failed", async () => {
  serve(
    watch({
      status: "rollback_failed",
      verdict: "fail",
      reason: "the rollback deployment never started",
      unwatched: [],
      unverified: false,
      rollback_plan_id: "plan_rb",
      settled_at: "2026-10-29T15:10:00Z",
    })
  )
  expect(await screen.findByText("Rollback failed")).toBeInTheDocument()
  const alert = screen.getByRole("alert")
  expect(alert).toHaveTextContent(
    "This release breached its health checks, and the rollback failed. Check the rollback, then retry it or deploy a fix."
  )
  expect(alert).toHaveTextContent("the rollback deployment never started")
  expect(
    within(alert).getByRole("link", { name: "Open the rollback" })
  ).toHaveAttribute("href", "/deployments/plan_rb")
  expect(screen.queryByText("Unverified")).not.toBeInTheDocument()
})

it("shows nothing for a deployment with no watch", async () => {
  const fetch = serve({ message: "release watch not found" }, 404)
  await vi.waitFor(() => expect(fetch).toHaveBeenCalled())
  await new Promise((r) => setTimeout(r, 50))
  expect(
    screen.queryByRole("region", { name: "Release watch" })
  ).not.toBeInTheDocument()
})
