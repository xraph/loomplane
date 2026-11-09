import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { cloudSettingsGroups, forgetOwner, onCloudChange } from "./index"

const ctx = (member: boolean) => ({
  at: () => false,
  can: (p: string) => member && p === "member:manage",
})

function answer(status: number) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(status === 204 ? null : "{}", { status }))
  )
}

const platform = (m: boolean) =>
  cloudSettingsGroups(ctx(m)).find((g) => g.label === "Platform")?.items ?? []

beforeEach(forgetOwner)
afterEach(() => vi.unstubAllGlobals())

it("offers platform billing only to the platform owner", async () => {
  answer(404)
  expect(platform(true)).toEqual([])
  await vi.waitFor(() => expect(fetch).toHaveBeenCalled())
  await new Promise((r) => setTimeout(r, 10))
  expect(platform(true)).toEqual([])

  forgetOwner()
  answer(204)
  const changed = new Promise<void>((resolve) => {
    const off = onCloudChange(() => {
      off()
      resolve()
    })
  })
  expect(platform(true)).toEqual([])
  await changed
  expect(platform(true).map((i) => i.title)).toEqual(["Billing"])
})

it("offers workspace billing to whoever manages members", () => {
  answer(404)
  const workspace = (m: boolean) =>
    cloudSettingsGroups(ctx(m)).find((g) => g.label === "Workspace")?.items ??
    []
  expect(workspace(true).map((i) => i.title)).toEqual(["Billing"])
  expect(workspace(false)).toEqual([])
})
