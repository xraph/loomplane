import { expect, test } from "vitest"

import type {
  Canary,
  CanaryVerdict,
  Release,
} from "@/client/forge/loomplane/types"
import { type Run, toRun } from "@/lib/runs"
import { releaseSummary, showsCanary, stepStates } from "@/lib/canary"

import { canary, thresholds, verdict } from "@/lib/canary.fixtures"

const states = (c: Canary, v: CanaryVerdict[]) =>
  stepStates(c, v).map((s) => s.state)

test("releaseSummary reads like the manifest", () => {
  const release: Release = {
    strategy: "canary",
    steps: [5, 25, 50, 100],
    holdSeconds: 600,
    thresholds,
  }
  expect(releaseSummary(release)).toBe(
    "Canary: 5%, 25%, 50%, 100%, holding 10 minutes at each step"
  )
  expect(
    releaseSummary({ ...release, holdSeconds: 90, steps: [10, 100] })
  ).toBe("Canary: 10%, 100%, holding 90 seconds at each step")
})

test("stepStates marks where the ramp is", () => {
  expect(
    states(canary({ step: 1, weight: 25 }), [verdict({ step: 0 })])
  ).toEqual(["passed", "holding", "pending", "pending"])
  expect(
    states(canary({ status: "pending", step: -1, weight: 0 }), [])
  ).toEqual(["pending", "pending", "pending", "pending"])
})

test("an inconclusive hold stays holding until its bound", () => {
  const held = [
    verdict({ id: "d/web/0/0", attempt: 0, verdict: "inconclusive" }),
    verdict({ id: "d/web/0/1", attempt: 1, verdict: "inconclusive" }),
  ]
  expect(states(canary({}), held)[0]).toBe("holding")
  const bound = [
    ...held,
    verdict({ id: "d/web/0/2", attempt: 2, verdict: "inconclusive" }),
    verdict({ id: "d/web/0/3", attempt: 3, verdict: "inconclusive" }),
  ]
  expect(states(canary({ status: "rolled_back" }), bound)[0]).toBe(
    "inconclusive"
  )
})

test("a failed step is where the rollback stopped", () => {
  expect(
    states(canary({ status: "rolled_back", step: 1, weight: 25 }), [
      verdict({ step: 0 }),
      verdict({ id: "d/web/1/0", step: 1, weight: 25, verdict: "fail" }),
    ])
  ).toEqual(["passed", "failed", "pending", "pending"])
})

test("a failed promotion passes every step only when no step failed", () => {
  const passing = [0, 1, 2, 3].map((step) =>
    verdict({ id: `d/web/${step}/0`, step })
  )
  expect(
    states(canary({ status: "promote_failed", step: 3, weight: 0 }), passing)
  ).toEqual(["passed", "passed", "passed", "passed"])
  // A retry failed its second step after an earlier attempt promoted some
  // services, so the canary ended promote_failed with a failed step.
  expect(
    states(canary({ status: "promote_failed", step: 1, weight: 0 }), [
      verdict({ step: 0 }),
      verdict({ id: "d/web/1/0", step: 1, weight: 25, verdict: "fail" }),
    ])
  ).toEqual(["passed", "failed", "pending", "pending"])
  // It ran out of holds at its third step.
  expect(
    states(canary({ status: "promote_failed", step: 2, weight: 0 }), [
      verdict({ step: 0 }),
      verdict({ id: "d/web/1/0", step: 1, weight: 25 }),
      verdict({
        id: "d/web/2/0",
        step: 2,
        weight: 50,
        verdict: "inconclusive",
      }),
    ])
  ).toEqual(["passed", "passed", "inconclusive", "pending"])
})

test("a promoted canary passed every step", () => {
  expect(
    states(canary({ status: "promoted", step: 3, weight: 100 }), [])
  ).toEqual(["passed", "passed", "passed", "passed"])
})

test("a pass at the current step counts as passed before the ramp moves on", () => {
  expect(
    states(canary({ step: 0 }), [verdict({ step: 0, verdict: "pass" })])
  ).toEqual(["passed", "pending", "pending", "pending"])
  expect(states(canary({ step: 0 }), [])).toEqual([
    "holding",
    "pending",
    "pending",
    "pending",
  ])
})

const planOf = (over: object) =>
  ({ release: { strategy: "canary", canary: ["web"] }, ...over }) as never

test("showsCanary waits for a plan that can ramp, then follows the deployment", () => {
  const run = (over: object) => over as Run
  expect(showsCanary(run({ status: "waiting", plan: planOf({}) }))).toBe(true)
  expect(showsCanary(run({ status: "blocked", plan: planOf({}) }))).toBe(false)
  expect(showsCanary(run({ status: "canceled", plan: planOf({}) }))).toBe(false)
  expect(
    showsCanary(
      run({
        status: "waiting",
        plan: planOf({ release: { strategy: "canary", canary: [] } }),
      })
    )
  ).toBe(false)
  expect(
    showsCanary(
      run({
        status: "running",
        plan: planOf({}),
        deployment: { canary: canary({}) },
      })
    )
  ).toBe(true)
  expect(
    showsCanary(run({ status: "running", plan: planOf({}), deployment: {} }))
  ).toBe(false)
})

test("a ramping step reads as taking a share of traffic", () => {
  const plan = {
    id: "p",
    status: "approved",
    actions: [{ id: "web", op: "update", name: "web" }],
    warnings: [],
    problems: [],
  } as never
  const run = toRun(plan, {
    id: "d",
    status: "running",
    steps: [{ actionId: "web", status: "ramping" }],
  } as never)
  const step = run.thread.find((s) => s.key === "web")
  expect(step).toMatchObject({
    state: "running",
    detail: "Taking a share of traffic",
  })
})
