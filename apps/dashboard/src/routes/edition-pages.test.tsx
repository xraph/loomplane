import { afterEach, expect, it, vi } from "vitest"

afterEach(() => {
  vi.unstubAllEnvs()
  vi.resetModules()
})

it("is not found in the public edition", async () => {
  vi.stubEnv("VITE_LOOMPLANE_EDITION", undefined)
  const { Route } = await import("./settings.$page")
  expect(() => Route.options.beforeLoad?.({} as never)).toThrow()
  const platform = await import("./platform.$page")
  expect(() => platform.Route.options.beforeLoad?.({} as never)).toThrow()
})

it("loads in the cloud edition", async () => {
  vi.stubEnv("VITE_LOOMPLANE_EDITION", "cloud")
  const { Route } = await import("./settings.$page")
  expect(() => Route.options.beforeLoad?.({} as never)).not.toThrow()
  const platform = await import("./platform.$page")
  expect(() => platform.Route.options.beforeLoad?.({} as never)).not.toThrow()
})
