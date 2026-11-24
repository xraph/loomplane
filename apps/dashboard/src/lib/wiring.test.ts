import { expect, test } from "vitest"

import {
  accessOptions,
  accessProblem,
  choiceOf,
  envOf,
  handSetFor,
  ownUserName,
  prefixTemplate,
  suggestWiring,
  templatesFor,
  wirable,
  wiringBody,
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

test("each access mode says what it needs of a workspace service", () => {
  const rights = {
    engine: "postgres",
    registered: true,
    createDatabase: false,
    adminSet: false,
    adminCreateDatabase: false,
    adminCreateUser: false,
  }
  const [shared, ownDatabase, ownUser] = accessOptions(rights, "api")
  expect(shared.unavailable).toBe("")
  expect(ownDatabase.note).toMatch(/api must already exist/)
  expect(ownUser.unavailable).toMatch(/admin credential/)
  expect(
    accessOptions({ ...rights, engine: "redis" }, "1")[2].unavailable
  ).toMatch(/Postgres and MySQL only/)
  expect(
    accessProblem({ access: "own_database", database: "x" }, "redis")
  ).toMatch(/numbered/)
  expect(
    accessProblem({ access: "own_user", database: "api-db" }, "postgres")
  ).toMatch(/letters, digits and underscores/)
})

test("a wiring is written only when it changes something", () => {
  const one = { mode: "one" as const, key: "DATABASE_URL" }
  expect(wiringBody(one, "DATABASE_URL")).toBeUndefined()
  expect(
    wiringBody(one, "DATABASE_URL", { access: "shared", database: "api" })
  ).toBeUndefined()
  expect(
    wiringBody(one, "DATABASE_URL", { access: "own_database", database: "api" })
  ).toEqual({ access: "own_database", database: "api" })
  expect(wiringBody({ mode: "one", key: "DB" }, "DATABASE_URL")).toEqual({
    env: { DB: "url" },
  })
})

test("a wiring reads back into the form", () => {
  expect(choiceOf("postgres", "DATABASE_URL")).toEqual({
    mode: "one",
    key: "DATABASE_URL",
  })
  expect(choiceOf("postgres", "DATABASE_URL", { DSN: "url" })).toEqual({
    mode: "one",
    key: "DSN",
  })
  expect(
    choiceOf("postgres", "DATABASE_URL", {
      PGHOST: "host",
      PGPORT: "port",
      PGUSER: "user",
      PGPASSWORD: "password",
      PGDATABASE: "database",
    })
  ).toMatchObject({ mode: "parts", template: "libpq" })
  expect(
    choiceOf("postgres", "DATABASE_URL", { DBHOST: "host" })
  ).toMatchObject({
    mode: "parts",
    template: "custom",
    vars: { host: "DBHOST" },
  })
})

test("the user Loomplane makes is named, and variables set by hand found", () => {
  expect(ownUserName("shop", "api-v2")).toBe("lp_shop_api_v2")
  expect(
    ownUserName("a-very-long-project-name", "billing-worker")
  ).toBeUndefined()
  expect(
    handSetFor(
      "api",
      ["PGHOST", "PGUSER"],
      [
        { key: "PGHOST", service: "api", environment: "production" },
        { key: "PGUSER", service: "worker", environment: "" },
        { key: "PGUSER", environment: "", resourceOutput: "orders.url" },
        { key: "PGHOST", environment: "" },
      ]
    )
  ).toEqual([
    { key: "PGHOST", environment: "production" },
    { key: "PGHOST", environment: "" },
  ])
})
