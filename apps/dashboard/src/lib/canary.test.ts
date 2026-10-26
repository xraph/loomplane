import { expect, test } from "vitest"

import type {
  Canary,
  CanaryVerdict,
  Release,
} from "@/client/forge/loomplane/types"
import { releaseSummary, stepStates } from "@/lib/canary"

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

test("a promoted canary passed every step", () => {
  expect(
    states(canary({ status: "promoted", step: 3, weight: 100 }), [])
  ).toEqual(["passed", "passed", "passed", "passed"])
})
