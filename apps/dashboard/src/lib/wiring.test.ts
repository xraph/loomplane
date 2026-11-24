import { expect, test } from "vitest"

import {
  envOf,
  prefixTemplate,
  suggestWiring,
  templatesFor,
  wirable,
  wiringProblem,
} from "@/lib/wiring"

test("the form starts on what the code reads", () => {
  // Two or more of the engine's own variables: those, apart.
  expect(
    suggestWiring("postgres", "DATABASE_URL", ["PGHOST", "pgpassword", "PORT"])
  ).toEqual({
    choice: {
      mode: "parts",
      template: "libpq",
      prefix: "DB_",
      vars: { host: "PGHOST", password: "PGPASSWORD" },
    },
    detected: ["PGHOST", "PGPASSWORD"],
  })
  // DB_HOST and DB_PASSWORD under the usual prefix.
  expect(
    suggestWiring("mysql", "DATABASE_URL", [
      "DB_HOST",
      "DB_PASSWORD",
      "DB_NAME",
    ]).choice
  ).toEqual({
    mode: "parts",
    template: "prefix",
    prefix: "DB_",
    vars: { host: "DB_HOST", password: "DB_PASSWORD", database: "DB_NAME" },
  })
  // A whole connection string under one of the usual names.
  expect(suggestWiring("postgres", "DATABASE_URL", ["PG_URL", "PORT"])).toEqual(
    { choice: { mode: "one", key: "PG_URL" }, detected: ["PG_URL"] }
  )
  expect(suggestWiring("valkey", "REDIS_URL", ["CACHE_URL"]).choice).toEqual({
    mode: "one",
    key: "CACHE_URL",
  })
  // Nothing it recognizes: the kind's own.
  expect(suggestWiring("mongodb", "MONGODB_URI", ["PORT"])).toEqual({
    choice: { mode: "one", key: "MONGODB_URI" },
    detected: [],
  })
})

test("only a choice other than the kind's own writes wiring", () => {
  expect(
    envOf({ mode: "one", key: "DATABASE_URL" }, "DATABASE_URL")
  ).toBeUndefined()
  expect(envOf({ mode: "one", key: " PG_URL " }, "DATABASE_URL")).toEqual({
    PG_URL: "url",
  })
  expect(
    envOf(
      {
        mode: "parts",
        template: "libpq",
        prefix: "DB_",
        vars: { host: "PGHOST", port: "", password: "PGPASSWORD" },
      },
      "DATABASE_URL"
    )
  ).toEqual({
    PGHOST: "host",
    PGPASSWORD: "password",
  })
})

test("templates and names", () => {
  expect(
    wirable("mariadb") &&
      wirable("valkey") &&
      !wirable("object-storage") &&
      !wirable(undefined)
  ).toBe(true)
  expect(templatesFor("redis").map((t) => t.id)).toEqual(["redis", "prefix"])
  expect(prefixTemplate("orders-").vars).toEqual({
    host: "ORDERSHOST",
    port: "ORDERSPORT",
    user: "ORDERSUSER",
    password: "ORDERSPASSWORD",
    database: "ORDERSNAME",
  })
  expect(wiringProblem({ mode: "one", key: "1DB" })).toMatch(
    /isn't a variable name/
  )
  expect(
    wiringProblem({ mode: "parts", template: "libpq", prefix: "", vars: {} })
  ).toBe("Name at least one variable.")
  expect(
    wiringProblem({
      mode: "parts",
      template: "libpq",
      prefix: "",
      vars: { host: "DB", port: "DB" },
    })
  ).toBe("DB is used twice.")
  expect(wiringProblem({ mode: "one", key: "DATABASE_URL" })).toBe("")
})
