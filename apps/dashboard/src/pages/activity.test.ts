import { describe, expect, it } from "vitest"

import { duration } from "./activity"

describe("duration", () => {
  it("says seconds the way a person would", () => {
    expect(duration(42)).toBe("42s")
    expect(duration(60)).toBe("1m")
    expect(duration(95)).toBe("1m 35s")
    expect(duration(3600)).toBe("1h")
    expect(duration(3720)).toBe("1h 2m")
  })
})
