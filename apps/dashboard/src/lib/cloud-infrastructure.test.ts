import { expect, it } from "vitest"
import { validExternalTTL, canUpdateExternalTTL } from "./cloud-infrastructure"
it.each([0, 1, 59, 86401, -1, 60.5, NaN, Infinity])(
  "refuses unsupported TTL %s",
  (seconds) => {
    expect(validExternalTTL(seconds)).toBe(false)
  }
)
it.each([60, 300, 86400])("accepts reviewed TTL %s", (seconds) => {
  expect(validExternalTTL(seconds)).toBe(true)
})
it.each(["pending", "waiting", "failed", "removed"])(
  "does not update external TTL while %s",
  (status) => {
    expect(canUpdateExternalTTL(status, "external")).toBe(false)
  }
)
it("updates only ready external DNS routes", () => {
  expect(canUpdateExternalTTL("ready", "managed")).toBe(false)
  expect(canUpdateExternalTTL("ready", undefined)).toBe(false)
  expect(canUpdateExternalTTL("ready", "external")).toBe(true)
})
