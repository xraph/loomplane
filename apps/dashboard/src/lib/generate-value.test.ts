import { describe, expect, it } from "vitest"

import { generatorFor, generators, password } from "./generate-value"

const byId = (id: string) => generators.find((g) => g.id === id)!

describe("generators", () => {
  it("make values of the size openssl would", () => {
    expect(byId("hex32").generate()).toMatch(/^[0-9a-f]{64}$/)
    expect(byId("hex16").generate()).toMatch(/^[0-9a-f]{32}$/)
    expect(atob(byId("base64-32").generate())).toHaveLength(32)
    expect(atob(byId("base64-64").generate())).toHaveLength(64)
    expect(byId("base64url-32").generate()).toMatch(/^[A-Za-z0-9_-]{43}=$/)
    expect(byId("uuid").generate()).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
    )
  })

  it("never repeat", () => {
    expect(byId("hex32").generate()).not.toBe(byId("hex32").generate())
  })

  it("make passwords without look-alikes", () => {
    const p = password(200)
    expect(p).toHaveLength(200)
    expect(p).not.toMatch(/[0O1lI]/)
  })

  it("pick the one a key most likely wants", () => {
    expect(generatorFor("DB_PASSWORD").id).toBe("password")
    expect(generatorFor("INSTANCE_UUID").id).toBe("uuid")
    expect(generatorFor("SESSION_SECRET").id).toBe("hex32")
  })
})
