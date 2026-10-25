import { afterEach, describe, expect, it, vi } from "vitest"

import {
  ACCENT,
  ACCENTS,
  applyAccent,
  isAccent,
  resolveAccent,
  storedAccent,
} from "./accent"

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe("accent", () => {
  it("keeps the build default when a saved preference is absent or invalid", () => {
    vi.stubEnv("VITE_LOOMPLANE_ACCENT", "plum")
    expect(storedAccent()).toBe("plum")
    localStorage.setItem("lp-accent", "invalid")
    expect(storedAccent()).toBe("plum")
    localStorage.setItem("lp-accent", "sage")
    expect(storedAccent()).toBe("sage")
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied")
    })
    expect(storedAccent()).toBe("plum")
  })
  it("defaults to a known accent", () => {
    expect(ACCENTS).toContain(ACCENT)
  })

  it("takes a build override only when it names a known accent", () => {
    expect(resolveAccent("plum")).toBe("plum")
    expect(resolveAccent("ocean")).toBe("ocean")
    expect(resolveAccent("copper")).toBe("copper")
    expect(resolveAccent("sage")).toBe("sage")
    expect(resolveAccent("unknown")).toBe(ACCENT)
    expect(resolveAccent(undefined)).toBe(ACCENT)
    expect(isAccent(3)).toBe(false)
  })

  it("marks the root element", () => {
    const root = document.createElement("html")
    applyAccent("ocean", root)
    expect(root.dataset.accent).toBe("ocean")
  })
})
