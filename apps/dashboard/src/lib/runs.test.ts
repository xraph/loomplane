import { describe, expect, it } from "vitest"

import type { Action, Deployment, Plan } from "@/client/forge/loomplane/types"
import { commitLabel, matches, runLog, toRun } from "@/lib/runs"

const action = (id: string, step: number, op = "create"): Action => ({
  id,
  step,
  op,
  name: id,
  creates: "Cloud Run service",
  engine: "ctrlplane",
  provider: "cloudrun",
  reason: "",
  subject: "service",
})

const plan = (status: string, extra: Partial<Plan> = {}): Plan => ({
  catalog: false,
  id: "plan_1",
  orgId: "org_1",
  projectId: "proj_1",
  environment: "production",
  environmentId: "env_1",
  status,
  actions: [action("api", 1), action("web", 2)],
  artifacts: [],
  fromEnvironment: "",
  fromEnvironmentId: "",
  kind: "release",
  leftovers: [],
  connections: {},
  createdAt: "2026-10-12T10:00:00Z",
  createdBy: "user_1",
  decidedBy: "",
  decisionNote: "",
  error: "",
  executable: true,
  manifestId: "man_1",
  manifestRevision: 3,
  problems: [],
  warnings: [],
  revision: "r1",
  sources: [
    {
      alias: "app",
      analysisId: "an_1",
      commitSha: "0123456789abcdef",
      inScope: true,
      repository: "acme/app",
      root: ".",
      sourceId: "src_1",
    },
  ],
  summary: { create: 2, update: 0, replace: 0, delete: 0, keep: 0 },
  ...extra,
})

const deployment = (
  status: string,
  steps: { actionId: string; status: string }[]
): Deployment => ({
  id: "dep_1",
  orgId: "org_1",
  planId: "plan_1",
  projectId: "proj_1",
  environment: "production",
  environmentId: "env_1",
  commitSha: "0123456789abcdef",
  error: "",
  retries: 0,
  sources: [],
  startedAt: "2026-10-12T10:05:00Z",
  startedBy: "user_1",
  status,
  steps: steps.map((s, i) => ({
    actionId: s.actionId,
    status: s.status,
    order: i,
    name: s.actionId,
    op: "create",
    provider: "cloudrun",
    subject: "service",
    image: "",
    message: "",
    url: "",
    startedAt: "2026-10-12T10:05:00Z",
  })),
})

describe("toRun", () => {
  it.each([
    ["planning", "running"],
    ["ready", "waiting"],
    ["blocked", "blocked"],
    ["failed", "failed"],
    ["rejected", "canceled"],
    ["expired", "canceled"],
    ["superseded", "canceled"],
    ["approved", "running"],
  ])("a %s plan with no deployment is %s", (status, want) => {
    expect(toRun(plan(status)).status).toBe(want)
  })

  it("an approved plan that can't run is approved, not running", () => {
    expect(toRun(plan("approved", { executable: false })).status).toBe(
      "approved"
    )
  })

  it("takes the deployment's status once there is one", () => {
    const p = plan("approved")
    expect(toRun(p, deployment("running", [])).status).toBe("running")
    expect(toRun(p, deployment("succeeded", [])).status).toBe("succeeded")
    expect(toRun(p, deployment("failed", [])).status).toBe("failed")
  })
})

describe("thread", () => {
  it("lays out plan, approval and one step per action, in step order", () => {
    const r = toRun(
      plan("ready", { actions: [action("web", 2), action("api", 1)] })
    )
    expect(r.thread.map((s) => [s.group, s.state])).toEqual([
      ["Plan", "done"],
      ["Approve", "waiting"],
      ["Deploy", "pending"],
      ["Deploy", "pending"],
    ])
    expect(r.thread.slice(2).map((s) => s.key)).toEqual(["api", "web"])
  })

  it("maps deployment steps onto the plan's actions", () => {
    const r = toRun(
      plan("approved"),
      deployment("running", [
        { actionId: "api", status: "succeeded" },
        { actionId: "web", status: "building" },
      ])
    )
    expect(r.thread.map((s) => s.state)).toEqual([
      "done",
      "done",
      "done",
      "running",
    ])
    expect(r.phase).toBe("Building: Create web")
  })

  it("names the step a failed deployment stopped at", () => {
    const r = toRun(
      plan("approved"),
      deployment("failed", [
        { actionId: "api", status: "failed" },
        { actionId: "web", status: "skipped" },
      ])
    )
    expect(r.phase).toBe("Failed at create api")
  })

  it("draws the deploy steps of a rejected plan as never run", () => {
    const r = toRun(plan("rejected"))
    expect(r.thread.map((s) => s.state)).toEqual([
      "done",
      "canceled",
      "canceled",
      "canceled",
    ])
  })

  it("has one step when there's nothing to change", () => {
    const r = toRun(
      plan("approved", { actions: [] }),
      deployment("succeeded", [])
    )
    expect(r.thread.at(-1)?.name).toBe("Nothing to change")
    expect(r.thread.at(-1)?.state).toBe("done")
  })
})

it("does not queue kept services as deployment work", () => {
  const kept = action("cache", 1, "keep")
  const created = action("browser-cache", 2)
  const run = toRun(
    plan("approved", { actions: [kept, created] }),
    deployment("succeeded", [
      { actionId: "browser-cache", status: "succeeded" },
    ])
  )
  expect(
    run.thread.filter((s) => s.group === "Deploy").map((s) => [s.key, s.state])
  ).toEqual([["browser-cache", "done"]])
  const unchanged = toRun(
    plan("approved", { actions: [kept] }),
    deployment("succeeded", [])
  )
  expect(unchanged.thread.at(-1)?.name).toBe("Nothing to change")
  expect(unchanged.thread.at(-1)?.state).toBe("done")
})

describe("runLog", () => {
  it("tells the story in time order, with the decision by name", () => {
    const r = toRun(
      plan("approved", {
        plannedAt: "2026-10-12T10:01:00Z",
        decidedAt: "2026-10-12T10:04:00Z",
        decidedBy: "user_2",
        decisionNote: "ship it",
      }),
      deployment("running", [{ actionId: "api", status: "building" }])
    )
    const lines = runLog(r, (id) => (id === "user_2" ? "Grace" : "Ada"))
    const texts = lines.map((l) => l.text)
    expect(texts[0]).toContain("Ada asked for a plan of production")
    expect(texts).toContain("Grace approved the plan: ship it")
    expect(lines.map((l) => l.ts)).toEqual(
      [...lines.map((l) => l.ts)].sort((a, b) => a - b)
    )
  })

  it("logs problems as errors on the approval step", () => {
    const r = toRun(
      plan("blocked", {
        plannedAt: "2026-10-12T10:01:00Z",
        problems: [
          { code: "no-connection", message: "No Google Cloud connection" },
        ],
      })
    )
    const problem = runLog(r, () => "Ada").find((l) => l.level === "error")
    expect(problem?.text).toBe("No Google Cloud connection")
    expect(problem?.step).toBe("approve")
  })
})

describe("helpers", () => {
  it("labels one repository's commit by its short SHA", () => {
    expect(commitLabel(toRun(plan("ready")))).toBe("0123456")
  })

  it("finds a run by commit, environment or ID", () => {
    const r = toRun(plan("ready"))
    expect(matches(r, "0123456")).toBe(true)
    expect(matches(r, "PRODUCTION")).toBe(true)
    expect(matches(r, "plan_1")).toBe(true)
    expect(matches(r, "staging")).toBe(false)
  })
})
