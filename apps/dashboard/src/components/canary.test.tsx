import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import type {
  CanaryReport,
  Plan,
  Release,
} from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ApprovalCard } from "@/components/approval"
import { CanaryPanel, CanaryTimeline } from "@/components/canary"
import { toRun } from "@/lib/runs"
import { canary, verdict } from "@/lib/canary.fixtures"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
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

const thresholds = {
  errorRate: 0.02,
  errorRateDelta: 0.01,
  minRequests: 200,
  p95LatencyRatio: 1.5,
}
const release: Release = {
  strategy: "canary",
  steps: [5, 25, 50, 100],
  holdSeconds: 600,
  thresholds,
  canary: ["svc-web"],
}

function serve(report: unknown) {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify(report), {
        headers: { "Content-Type": "application/json" },
      })
  )
}

function panel(report: CanaryReport) {
  serve(report)
  render(
    <ClientProvider client={forgeClient}>
      <CanaryPanel deploymentId="dep_1" release={release} />
    </ClientProvider>
  )
}

test("CanaryTimeline lists each service's verdict with errors and latency", () => {
  const failing = verdict({
    verdict: "fail",
    candidate: {
      available: true,
      errors: 32,
      requests: 1000,
      latencyMs: 140,
      statistic: "p95",
    },
  })
  const azure = verdict({
    id: "d/api/0/0",
    actionId: "api",
    service: "api",
    candidate: {
      available: true,
      errors: 0,
      requests: 500,
      latencyMs: 80,
      statistic: "average",
    },
  })
  render(<CanaryTimeline canary={canary({})} verdicts={[failing, azure]} />)
  expect(
    screen.getByRole("row", { name: /web.*fail.*3\.2%.*140ms p95/ })
  ).toBeInTheDocument()
  expect(screen.getByRole("row", { name: /api.*80ms avg/ })).toBeInTheDocument()
})

test("a failed step and the rollback are shown with the canary's reason", async () => {
  panel({
    canary: canary({
      status: "rolled_back",
      step: 1,
      weight: 0,
      reason: "The canary failed at 25%: web's errors rose to 3.2%.",
    }),
    verdicts: [
      verdict({}),
      verdict({ id: "d/web/1/0", step: 1, weight: 25, verdict: "fail" }),
    ],
  })
  expect(
    await screen.findByText(/The canary failed at 25%/)
  ).toBeInTheDocument()
  const steps = within(screen.getByRole("list", { name: "Ramp" }))
  expect(steps.getAllByRole("listitem").map((li) => li.textContent)).toEqual([
    "5%Passed",
    "25%Failed",
    "50%Waiting",
    "100%Waiting",
  ])
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})

test.each(["revert_failed", "promote_failed"])(
  "%s says a person has to act, in the failed colour",
  async (status) => {
    panel({
      canary: canary({
        status,
        step: 1,
        reason:
          "Traffic may still be split: web's candidate couldn't be removed.",
      }),
      verdicts: [],
    })
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("Needs you")
    expect(alert).toHaveTextContent("Traffic may still be split")
    expect(alert.className).toContain("text-destructive")
    expect(alert.className).toContain("bg-failed-tint")
  }
)

test("a release with no deployment yet says the ramp starts on approval", () => {
  render(<CanaryPanel release={release} />)
  expect(screen.getByRole("heading", { name: "Canary" })).toBeInTheDocument()
  expect(screen.getByText(/Canary: 5%, 25%, 50%, 100%/)).toBeInTheDocument()
})

function waitingPlan(over: Partial<Plan>): Plan {
  return {
    id: "plan_1",
    actions: [
      { id: "svc-web", name: "web" },
      { id: "svc-worker", name: "worker" },
    ],
    environment: "production",
    status: "waiting",
    executable: true,
    summary: { adopt: 0, create: 0, delete: 0, keep: 0, replace: 0, update: 2 },
    warnings: [],
    problems: [],
    sources: [],
    revision: "r1",
    ...over,
  } as unknown as Plan
}

function card(plan: Plan) {
  render(<ApprovalCard run={toRun(plan)} />)
}

test("the approval card says approving starts the canary", () => {
  card(waitingPlan({ release }))
  expect(screen.getByText("Approving starts a canary")).toBeInTheDocument()
  expect(
    screen.getByText(/Canary: 5%, 25%, 50%, 100%, holding 10 minutes/)
  ).toBeInTheDocument()
  expect(screen.getByText(/for web\./)).toBeInTheDocument()
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
    "Reject",
    "Approve and deploy",
  ])
})

test("a refused service keeps its warning and gets no canary line", () => {
  card(
    waitingPlan({
      release: { ...release, canary: [] },
      warnings: [
        {
          code: "canary-unavailable",
          name: "web",
          message: "web deploys all at once: Render has no traffic weighting.",
        },
      ],
    })
  )
  expect(
    screen.queryByText("Approving starts a canary")
  ).not.toBeInTheDocument()
  expect(
    screen.getByText(
      "web deploys all at once: Render has no traffic weighting."
    )
  ).toBeInTheDocument()
})
