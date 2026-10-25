import { describe, expect, it } from "vitest"

import { ACCENT, ACCENTS, applyAccent, isAccent, resolveAccent } from "./accent"

describe("accent", () => {
  it("defaults to a known accent", () => {
    expect(ACCENTS).toContain(ACCENT)
  })

  it("takes a build override only when it names a known accent", () => {
    expect(resolveAccent("plum")).toBe("plum")
    expect(resolveAccent("ocean")).toBe("ocean")
    expect(resolveAccent("copper")).toBe(ACCENT)
    expect(resolveAccent(undefined)).toBe(ACCENT)
    expect(isAccent(3)).toBe(false)
  })

  it("marks the root element", () => {
    const root = document.createElement("html")
    applyAccent("ocean", root)
    expect(root.dataset.accent).toBe("ocean")
  })
})
