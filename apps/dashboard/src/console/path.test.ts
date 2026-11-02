import { describe, expect, it } from "vitest"

import { isUnder } from "./path"

describe("isUnder", () => {
  it("matches the base itself, with or without a trailing slash", () => {
    expect(isUnder("/__console", "/__console")).toBe(true)
    expect(isUnder("/__console/", "/__console")).toBe(true)
  })

  it("matches anything below the base", () => {
    expect(isUnder("/__console/users/1", "/__console")).toBe(true)
  })

  it("does not match a sibling that shares the prefix", () => {
    expect(isUnder("/__consoles", "/__console")).toBe(false)
  })

  it("does not match unrelated paths", () => {
    expect(isUnder("/", "/__console")).toBe(false)
  })
})
