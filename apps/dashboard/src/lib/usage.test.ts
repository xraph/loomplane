import { expect, it } from "vitest"

import type { Amount, UsageSource } from "@/client/forge/loomplane/types"
import {
  change,
  headline,
  latencyLabel,
  money,
  notReporting,
  timeTicks,
} from "@/lib/usage"

const amount = (currency: string, total: number): Amount => ({
  currency,
  total,
  days: [],
  undated: 0,
})

it("writes money in its own currency, with cents only while they matter", () => {
  expect(money(12.5, "USD")).toBe("$12.50")
  expect(money(1234.56, "USD")).toBe("$1,235")
  expect(money(7, "EUR")).toBe("€7.00")
})

it("says how much something changed, or nothing when there was nothing before", () => {
  expect(change(120, 100)).toBe(20)
  expect(change(80, 100)).toBe(-20)
  expect(change(5, 0)).toBeNull()
})

it("leads with the largest currency and keeps the rest", () => {
  const { lead, rest } = headline([amount("EUR", 7), amount("USD", 35)])
  expect(lead?.currency).toBe("USD")
  expect(rest.map((a) => a.currency)).toEqual(["EUR"])
  expect(headline([]).lead).toBeUndefined()
})

it("never calls an average a p95", () => {
  expect(latencyLabel("p95")).toBe("p95")
  expect(latencyLabel("average")).toBe("avg")
  expect(latencyLabel(undefined)).toBe("")
})

it("lists only the sources that aren't reporting, each reason once", () => {
  const src = (
    connectionId: string,
    status: string,
    reason = "",
    services?: string[]
  ): UsageSource => ({
    connectionId,
    cloud: "gcp",
    name: connectionId,
    status,
    reason,
    services,
  })
  const notes = notReporting([
    src("a", "reporting"),
    src("b", "unavailable", "Grant Monitoring Viewer", ["api"]),
    src("b", "unavailable", "Grant Monitoring Viewer", ["web"]),
    src("c", "failed", "The cloud didn't answer."),
  ])
  expect(notes).toEqual([
    {
      key: "b:Grant Monitoring Viewer",
      connectionId: "b",
      name: "b",
      reason: "Grant Monitoring Viewer",
      failed: false,
      services: ["api", "web"],
    },
    {
      key: "c:The cloud didn't answer.",
      connectionId: "c",
      name: "c",
      reason: "The cloud didn't answer.",
      failed: true,
      services: [],
    },
  ])
})

it("puts one time tick on each edge worth naming, never two on one day", () => {
  const hour = 3_600_000
  const start = Date.UTC(2026, 9, 17)
  const grid = (step: number, n: number) =>
    Array.from({ length: n }, (_, i) => start + i * step)
  expect(timeTicks(grid(hour / 2, 48), "24h")).toEqual([
    start,
    start + 6 * hour,
    start + 12 * hour,
    start + 18 * hour,
  ])
  const week = timeTicks(grid(3 * hour, 56), "7d")
  expect(week).toHaveLength(7)
  expect(week.every((t) => t % (24 * hour) === 0)).toBe(true)
  expect(timeTicks(grid(12 * hour, 60), "30d")).toHaveLength(6)
  expect(timeTicks(grid(24 * hour, 90), "90d")).toHaveLength(6)
})
