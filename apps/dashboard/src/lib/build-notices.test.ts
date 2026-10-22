import { expect, it } from "vitest"

import { notices } from "./build-notices"

const appPlatform =
  "App Platform's build logs are on the app's Activity tab in DigitalOcean."

it("folds builds with the same reason into one note with a count", () => {
  expect(
    notices([
      { id: "a", actionId: "s1", state: "unavailable", message: appPlatform },
      { id: "b", actionId: "s2", state: "unavailable", message: appPlatform },
      { id: "c", actionId: "s3", state: "unavailable", message: appPlatform },
      { id: "d", actionId: "s4", state: "complete", message: "" },
      { id: "e", actionId: "s5", state: "partial", message: "Cut off" },
    ])
  ).toEqual([
    {
      key: `true:${appPlatform}`,
      unavailable: true,
      message: appPlatform,
      count: 3,
    },
    { key: "false:Cut off", unavailable: false, message: "Cut off", count: 1 },
  ])
})

it("keeps only the chosen step's builds", () => {
  const out = notices(
    [
      { id: "a", actionId: "s1", state: "unavailable", message: appPlatform },
      { id: "b", actionId: "s2", state: "unavailable", message: appPlatform },
    ],
    "s2"
  )
  expect(out).toHaveLength(1)
  expect(out[0]!.count).toBe(1)
})
