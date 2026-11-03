import { afterEach, describe, expect, it, vi } from "vitest"

afterEach(() => {
  vi.unstubAllEnvs()
  vi.resetModules()
})

describe("edition", () => {
  it("is the public edition unless the build says cloud", async () => {
    vi.stubEnv("VITE_LOOMPLANE_EDITION", "")
    const { isCloud } = await import("./edition")
    expect(isCloud).toBe(false)
  })

  it("is the cloud edition when the build says cloud", async () => {
    vi.stubEnv("VITE_LOOMPLANE_EDITION", "cloud")
    const { isCloud } = await import("./edition")
    expect(isCloud).toBe(true)
  })
})
