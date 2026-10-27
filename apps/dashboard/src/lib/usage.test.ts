import { expect, it } from "vitest"

import type { Amount, UsageSource } from "@/client/forge/loomplane/types"
import {
  change,
  headline,
  latencyLabel,
  money,
  usageNotes,
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

it("says each reason once, for every account it applies to", () => {
  const src = (
    connectionId: string,
    cloud: string,
    status: string,
    reason = "",
    services?: string[]
  ): UsageSource => ({
    connectionId,
    cloud,
    name: connectionId,
    status,
    reason,
    services,
  })
  const role = "Make the token as an owner."
  const { fix, unsupported } = usageNotes([
    src("ok", "aws", "reporting"),
    src("do", "digitalocean", "unavailable", role, ["api"]),
    src("billing", "digitalocean", "unavailable", role, ["web"]),
    src("gcp", "gcp", "failed", "The cloud didn't answer."),
    src("v1", "vercel", "unsupported", "Vercel has no API."),
    src("v2", "vercel", "unsupported", "Vercel has no API."),
    src("r", "render", "unsupported", "Render has no API."),
  ])
  expect(fix).toEqual([
    {
      key: role,
      reason: role,
      failed: false,
      accounts: [
        { connectionId: "do", name: "do" },
        { connectionId: "billing", name: "billing" },
      ],
      services: ["api", "web"],
    },
    {
      key: "The cloud didn't answer.",
      reason: "The cloud didn't answer.",
      failed: true,
      accounts: [{ connectionId: "gcp", name: "gcp" }],
      services: [],
    },
  ])
  expect(unsupported).toEqual(["Render", "Vercel"])
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
