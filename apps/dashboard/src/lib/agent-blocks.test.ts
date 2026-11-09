import { expect, it } from "vitest"

import {
  formatValue,
  isNumeric,
  parseChart,
  parseStats,
  parseTimeline,
  statusTone,
} from "@/lib/agent-blocks"

it("reads a chart, numbers given as strings included", () => {
  const r = parseChart(
    JSON.stringify({
      type: "stacked",
      title: "Deployments per week",
      x: "week",
      series: [
        { key: "ok", label: "Succeeded", tone: "good" },
        { key: "failed", tone: "critical" },
        { label: "no key" },
      ],
      data: [
        { week: "W43", ok: "4", failed: 1 },
        { week: "W44", ok: 2, failed: "n/a" },
        { nope: 1 },
      ],
    })
  )
  expect(r).toEqual({
    ok: true,
    value: {
      type: "stacked",
      title: "Deployments per week",
      x: "week",
      series: [
        { key: "ok", label: "Succeeded", tone: "good" },
        { key: "failed", label: "failed", tone: "critical" },
      ],
      data: [
        { week: "W43", ok: 4, failed: 1 },
        { week: "W44", ok: 2, failed: null },
      ],
    },
  })
})

it("finds the series itself, falls back to a bar, and caps what it draws", () => {
  const r = parseChart(
    JSON.stringify({
      type: "pie",
      x: "env",
      data: [
        { env: "prod", a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, name: "x" },
      ],
    })
  )
  expect(r.ok && r.value.type).toBe("bar")
  expect(r.ok && r.value.series.map((s) => s.key)).toEqual([
    "a",
    "b",
    "c",
    "d",
    "e",
    "f",
  ])
})

it("turns down a chart it can't draw", () => {
  expect(parseChart("{not json").ok).toBe(false)
  expect(parseChart(JSON.stringify({ x: "w", data: [] })).ok).toBe(false)
  expect(parseChart(JSON.stringify({ data: [{ w: 1 }] })).ok).toBe(false)
  expect(
    parseChart(JSON.stringify({ x: "w", data: [{ w: "a", v: "high" }] })).ok
  ).toBe(false)
})

it("reads stats and timelines, bare or wrapped", () => {
  expect(
    parseStats(
      JSON.stringify([
        {
          label: "Failed",
          value: 1204,
          tone: "critical",
          href: "/deployments",
        },
        { label: "No value" },
        { label: "Odd tone", value: "3", tone: "purple" },
      ])
    )
  ).toEqual({
    ok: true,
    value: [
      {
        label: "Failed",
        value: (1204).toLocaleString(),
        tone: "critical",
        href: "/deployments",
      },
      { label: "Odd tone", value: "3" },
    ],
  })
  const t = parseTimeline(
    JSON.stringify({
      events: [{ title: "v0.1.5", date: "2026-10-31", tag: "prod" }, {}],
    })
  )
  expect(t).toEqual({
    ok: true,
    value: [{ title: "v0.1.5", date: "2026-10-31", tag: "prod" }],
  })
  expect(parseStats("[]").ok).toBe(false)
})

it("reads status words and numbers in table cells", () => {
  expect(statusTone("Failed")).toBe("critical")
  expect(statusTone(" live ")).toBe("good")
  expect(statusTone("Waiting")).toBe("warning")
  expect(statusTone("Rolled back")).toBe("serious")
  expect(statusTone("Cancelled")).toBe("neutral")
  expect(statusTone("Failed to pull the image")).toBeUndefined()
  for (const n of ["1,204", "-3", "$12.40", "99.9%", "120 ms", "4.2 GB"])
    expect(isNumeric(n), n).toBe(true)
  for (const n of ["v1.2.3", "3f2c1a9", "W44", ""])
    expect(isNumeric(n), n).toBe(false)
})

it("writes values for an axis or a tooltip", () => {
  expect(formatValue(1234)).toBe((1234).toLocaleString())
  expect(formatValue(25_000, undefined, true)).toMatch(/25K/i)
  expect(formatValue(12.5, "%")).toBe("12.5%")
  expect(formatValue(3, "USD")).toBe("$3")
  expect(formatValue(120, "ms")).toBe("120 ms")
})
