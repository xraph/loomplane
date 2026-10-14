import { describe, expect, it } from "vitest"

import { bridgeFrom, horizon, joinPages, needsOlder } from "@/lib/pages"

const row = (id: string, at: string) => ({ id, at })

describe("joinPages", () => {
  it("joins pages in order and keeps a row once, as the newest page has it", () => {
    const joined = joinPages([
      { items: [row("c", "3"), row("b", "2")], next: "cur-b" },
      { items: [row("b", "stale"), row("a", "1")], next: "cur-a" },
    ])
    expect(joined.items).toEqual([row("c", "3"), row("b", "2"), row("a", "1")])
    expect(joined.next).toBe("cur-a")
    expect(joined.loading).toBe(false)
  })

  it("says there is nothing older once the last page has no next", () => {
    expect(joinPages([{ items: [row("a", "1")] }]).next).toBeUndefined()
  })

  it("has no next while a page is still loading", () => {
    const joined = joinPages([
      { items: [row("b", "2")], next: "cur-b" },
      undefined,
    ])
    expect(joined).toEqual({
      items: [row("b", "2")],
      next: undefined,
      loading: true,
    })
  })
})

describe("bridgeFrom", () => {
  it("bridges the rows a refreshed first page pushed past where older pages start", () => {
    // Older pages were read from cur-1; a new run since moved the first
    // page's end to cur-2, so the rows between are in neither.
    expect(bridgeFrom("cur-2", ["cur-1", "cur-0"])).toBe("cur-2")
  })

  it("needs no bridge before any older page, or while the first page hasn't moved", () => {
    expect(bridgeFrom("cur-1", [])).toBeUndefined()
    expect(bridgeFrom("cur-1", ["cur-1"])).toBeUndefined()
    expect(bridgeFrom(undefined, ["cur-1"])).toBeUndefined()
  })
})

describe("horizon", () => {
  it("is the newest point a project with more still has to load back to", () => {
    // Go drops a time's trailing zeros, so these don't sort as strings.
    expect(
      horizon([
        "2026-10-10T00:00:00Z",
        undefined,
        "2026-10-12T00:00:00.5Z",
        "2026-10-12T00:00:00Z",
      ])
    ).toBe("2026-10-12T00:00:00.5Z")
  })

  it("is undefined once every project is loaded to its first run", () => {
    expect(horizon([undefined, undefined])).toBeUndefined()
    expect(horizon([])).toBeUndefined()
  })
})

describe("needsOlder", () => {
  const plan = (id: string, createdAt: string, status = "approved") => ({
    id,
    createdAt,
    status,
  })
  const deps = (next: string | undefined, ...items: [string, string][]) => ({
    items: items.map(([planId, startedAt]) => ({ planId, startedAt })),
    next,
    loading: false,
  })

  it("reads older deployments while an approved plan's could be among them", () => {
    // p1's deployment hasn't loaded, and the loaded ones reach back no
    // further than p1, so it may be on the next page.
    const plans = [
      plan("p2", "2026-10-11T10:00:00Z"),
      plan("p1", "2026-10-11T09:00:00.12Z"),
    ]
    expect(
      needsOlder(deps("cur", ["p2", "2026-10-11T09:00:00.125Z"]), plans)
    ).toBe(true)
  })

  it("reads nothing more when every approved plan has its deployment", () => {
    // The usual case: each run's deployment is on the same page as its plan.
    const plans = [
      plan("p2", "2026-10-11T10:00:00Z"),
      plan("p1", "2026-10-11T09:00:00Z"),
      plan("p0", "2026-10-11T08:00:00Z", "rejected"),
    ]
    expect(
      needsOlder(
        deps(
          "cur",
          ["p2", "2026-10-11T10:01:00Z"],
          ["p1", "2026-10-11T09:01:00Z"]
        ),
        plans
      )
    ).toBe(false)
  })

  it("stops once the deployments reach past the plan, run out, or are loading", () => {
    const plans = [plan("p1", "2026-10-11T00:00:00Z")]
    expect(needsOlder(deps("cur", ["p9", "2026-10-10T23:59:59Z"]), plans)).toBe(
      false
    )
    expect(
      needsOlder(deps(undefined, ["p9", "2026-10-11T01:00:00Z"]), plans)
    ).toBe(false)
    expect(
      needsOlder(
        { ...deps("cur", ["p9", "2026-10-11T01:00:00Z"]), loading: true },
        plans
      )
    ).toBe(false)
  })
})
