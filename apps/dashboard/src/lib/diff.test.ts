import { describe, expect, it } from "vitest"

import { changeCount, diffLines, hunks } from "./diff"

describe("diffLines", () => {
  it("keeps what's the same and marks what changed", () => {
    const d = diffLines(
      "version: 1\nservices:\n  api: {source: a}\n",
      "version: 1\nservices:\n  api: {source: b}\n  web: {source: w}\n"
    )
    expect(d).toEqual([
      { kind: "same", text: "version: 1" },
      { kind: "same", text: "services:" },
      { kind: "removed", text: "  api: {source: a}" },
      { kind: "added", text: "  api: {source: b}" },
      { kind: "added", text: "  web: {source: w}" },
    ])
    expect(changeCount(d)).toEqual({ added: 2, removed: 1 })
  })

  it("handles nothing before and nothing after", () => {
    expect(diffLines("", "a\n")).toEqual([{ kind: "added", text: "a" }])
    expect(diffLines("a\n", "")).toEqual([{ kind: "removed", text: "a" }])
    expect(diffLines("a\n", "a\n")).toEqual([{ kind: "same", text: "a" }])
  })
})

describe("hunks", () => {
  const same = (n: number, from = 0) =>
    Array.from({ length: n }, (_, i) => ({
      kind: "same" as const,
      text: `l${from + i}`,
    }))

  it("folds long unchanged runs down to context around a change", () => {
    const d = [
      ...same(10),
      { kind: "added" as const, text: "new" },
      ...same(10, 10),
    ]
    const h = hunks(d, 2)
    expect(
      h.map((x) => (x.kind === "folded" ? x.count : x.lines.length))
    ).toEqual([8, 5, 8])
    expect(h[1]).toEqual({
      kind: "lines",
      lines: [...same(2, 8), { kind: "added", text: "new" }, ...same(2, 10)],
    })
  })

  it("keeps short runs between changes whole", () => {
    const d = [
      { kind: "removed" as const, text: "a" },
      ...same(3),
      { kind: "added" as const, text: "b" },
    ]
    expect(hunks(d, 2)).toEqual([{ kind: "lines", lines: d }])
  })

  it("doesn't fold a diff with no changes", () => {
    expect(hunks(same(20))).toEqual([{ kind: "lines", lines: same(20) }])
  })
})
