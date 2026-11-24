import { describe, expect, test } from "vitest"

import {
  buildConnection,
  connectionHints,
  connectionsIn,
  engineOf,
  maskConnection,
  parseConnection,
  suggestName,
} from "@/lib/connection-string"

describe("parseConnection", () => {
  test("reads a Postgres URL without giving back its password", () => {
    const p = parseConnection(
      " postgres://app:s3cret@db.example.com:6543/orders?sslmode=require "
    )
    expect(p).toMatchObject({
      engine: "postgres",
      host: "db.example.com",
      port: 6543,
      user: "app",
      hasPassword: true,
      database: "orders",
      tls: "require",
    })
    expect(JSON.stringify(p)).not.toContain("s3cret")
  })

  test("fills in each engine's port and TLS", () => {
    expect(parseConnection("postgresql://app:x@db/app")).toMatchObject({
      port: 5432,
      tls: undefined,
    })
    expect(
      parseConnection("mysql://app:x@db/app?sslmode=verify-full")
    ).toMatchObject({ port: 3306, tls: "verify-full" })
    expect(parseConnection("redis://:x@cache:6380/2")).toMatchObject({
      engine: "redis",
      port: 6380,
      database: "2",
      tls: "disable",
      user: "",
    })
    expect(parseConnection("rediss://default:x@cache")).toMatchObject({
      port: 6379,
      tls: "verify-full",
    })
    expect(
      parseConnection("mongodb://app:x@db/events?authSource=admin")
    ).toMatchObject({ port: 27017, tls: "disable", database: "events" })
    expect(
      parseConnection("mongodb+srv://app:x@cluster0.example.net/events")
    ).toMatchObject({ tls: "verify-full" })
    expect(
      parseConnection(
        "mongodb://app:x@db/?tls=true&tlsAllowInvalidCertificates=true"
      )
    ).toMatchObject({ tls: "require" })
  })

  test("names a replica set's first host and counts the rest", () => {
    const p = parseConnection(
      "mongodb://app:x@a.example:27017,b.example:27018,c.example/events?replicaSet=rs0"
    )
    expect(p?.host).toBe("a.example")
    expect(p?.hosts).toEqual([
      "a.example:27017",
      "b.example:27018",
      "c.example",
    ])
  })

  test("decodes an escaped user and keeps an IPv6 host whole", () => {
    expect(
      parseConnection("postgres://app%40corp:p%40ss@[::1]:5432/app")
    ).toMatchObject({ user: "app@corp", host: "::1", port: 5432 })
  })

  test("is undefined for anything that isn't a known engine's URL", () => {
    expect(parseConnection("https://example.com")).toBeUndefined()
    expect(parseConnection("just some text")).toBeUndefined()
    expect(engineOf("cassandra://db")).toBeUndefined()
  })
})

test("maskConnection hides only the password", () => {
  expect(maskConnection("postgres://app:s3cret@db:5432/app")).toBe(
    "postgres://app:••••••@db:5432/app"
  )
  expect(maskConnection("redis://cache:6379/0")).toBe("redis://cache:6379/0")
})

test("buildConnection escapes what it writes and says the TLS mode", () => {
  expect(
    buildConnection({
      engine: "postgres",
      host: "db.example.com",
      port: "",
      user: "app",
      password: "p@ss/w:rd",
      database: "orders",
      tls: "require",
    })
  ).toBe("postgres://app:p%40ss%2Fw%3Ard@db.example.com/orders?sslmode=require")
  expect(
    buildConnection({
      engine: "redis",
      host: "cache",
      port: "6380",
      user: "",
      password: "x",
      database: "1",
      tls: "verify-full",
    })
  ).toBe("rediss://:x@cache:6380/1")
  expect(
    buildConnection({
      engine: "mongodb",
      host: "db",
      port: "",
      user: "app",
      password: "x",
      database: "events",
      tls: "verify-full",
    })
  ).toBe("mongodb://app:x@db/events?tls=true&authSource=events")
  // What it builds reads back as what was typed.
  const back = parseConnection(
    buildConnection({
      engine: "mysql",
      host: "db",
      port: "3307",
      user: "app",
      password: "x",
      database: "shop",
      tls: "verify-full",
    })
  )
  expect(back).toMatchObject({
    engine: "mysql",
    host: "db",
    port: 3307,
    user: "app",
    database: "shop",
    tls: "verify-full",
  })
})

describe("connectionHints", () => {
  test("says a local address won't be reached from a server", () => {
    expect(
      connectionHints("postgres://app:x@localhost:5432/app?sslmode=disable")[0]
        .message
    ).toMatch(/localhost is the machine/)
  })

  test("offers to add the sslmode the server asks for", () => {
    const [hint] = connectionHints("postgres://app:x@db.example.com/app")
    expect(hint.fix).toEqual({
      label: "Add sslmode=require",
      dsn: "postgres://app:x@db.example.com/app?sslmode=require",
    })
    const [joined] = connectionHints(
      "postgres://app:x@db.example.com/app?application_name=web"
    )
    expect(joined.fix?.dsn).toBe(
      "postgres://app:x@db.example.com/app?application_name=web&sslmode=require"
    )
  })

  test("asks for a user, and an escaped @ in a password", () => {
    expect(
      connectionHints("mysql://db.example.com/app?sslmode=require").map(
        (h) => h.message
      )
    ).toContain("A MySQL connection string names its user.")
    expect(
      connectionHints(
        "postgres://app:p@ss@db.example.com/app?sslmode=require"
      ).map((h) => h.message)
    ).toContain("Escape any @ in the password as %40, so the host reads right.")
  })

  test("has nothing to say about a good string", () => {
    expect(
      connectionHints("postgres://app:x@db.example.com/app?sslmode=verify-full")
    ).toEqual([])
    expect(connectionHints("redis://:x@cache.example.com:6379/0")).toEqual([])
  })
})

test("connectionsIn finds the connection strings in pasted .env lines", () => {
  const found = connectionsIn(
    [
      "# from staging",
      "DATABASE_URL=postgres://app:x@db/app?sslmode=require",
      "PORT=8080",
      'REDIS_URL="redis://:y@cache:6379/0"',
    ].join("\n")
  )
  expect(found).toEqual([
    {
      key: "DATABASE_URL",
      value: "postgres://app:x@db/app?sslmode=require",
      engine: "postgres",
    },
    { key: "REDIS_URL", value: "redis://:y@cache:6379/0", engine: "redis" },
  ])
  expect(connectionsIn("mongodb://app:x@db/events")).toEqual([
    { key: "", value: "mongodb://app:x@db/events", engine: "mongodb" },
  ])
  expect(connectionsIn("PORT=8080")).toEqual([])
})

test("suggestName names a service from its database, else its host", () => {
  expect(
    suggestName(
      parseConnection(
        "postgres://app:x@db.example.com/Orders_Main?sslmode=require"
      )!
    )
  ).toBe("orders-main")
  expect(
    suggestName(parseConnection("redis://:x@cache-01.example.com:6379")!)
  ).toBe("cache-01")
})
