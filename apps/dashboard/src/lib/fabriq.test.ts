import { expect, test } from "vitest"

import {
  checkMessage,
  stackParts,
  ago,
  agoDay,
  createdLabel,
  fabriqKindFor,
  fabriqNeeds,
  fabriqSummary,
  fabriqUses,
  unlinkedServices,
  unreachableReason,
  unreachableText,
  type FabriqChoice,
} from "@/lib/fabriq"

const defaults: FabriqChoice = {
  mode: "server",
  pg: "new",
  redis: "new",
  falkor: "off",
  es: "off",
}

test("summary matches the mock for the defaults", () => {
  expect(fabriqSummary(defaults)).toBe(
    "Postgres 16 with TimescaleDB, pgvector and PostGIS, Redis, a migration job and the Fabriq server and the admin console."
  )
})

test("summary for library with existing engines and projections", () => {
  expect(
    fabriqSummary({
      mode: "library",
      pg: "existing",
      redis: "existing",
      falkor: "new",
      es: "new",
    })
  ).toBe(
    "A link to your Postgres, a link to your Redis, FalkorDB, Elasticsearch, a migration job and the Fabriq worker."
  )
})

test("check messages", () => {
  expect(
    checkMessage("postgres", {
      ok: false,
      version: "16.4",
      missing: ["postgis"],
    })
  ).toEqual({
    tone: "warn",
    text: "Connected to PostgreSQL 16.4. Missing extension: postgis. Ask your database admin to install it, or provision a new one.",
  })
  expect(
    checkMessage("postgres", {
      ok: false,
      version: "16.4",
      missing: ["postgis", "vector"],
    }).text
  ).toBe(
    "Connected to PostgreSQL 16.4. Missing extension: postgis, vector. Ask your database admin to install it, or provision a new one."
  )
  expect(checkMessage("redis", { ok: true, version: "7.4" })).toEqual({
    tone: "ok",
    text: "Connected to Redis 7.4. Ready to use.",
  })
})

test("other problems follow the version, and an unreachable endpoint shows the first", () => {
  expect(
    checkMessage("redis", {
      ok: false,
      version: "6.2",
      problems: ["Redis 6.2 is older than 7.", "Use a redis:// address."],
    })
  ).toEqual({
    tone: "warn",
    text: "Connected to Redis 6.2. Redis 6.2 is older than 7. Use a redis:// address.",
  })
  expect(
    checkMessage("postgres", {
      ok: false,
      problems: [
        "db.internal:5432 isn't reachable from the target: no answer within 5s",
        "A second problem.",
      ],
    })
  ).toEqual({
    tone: "warn",
    text: "db.internal:5432 isn't reachable from the target: no answer within 5s",
  })
})

test("the kind follows the mode", () => {
  expect(fabriqKindFor("server")).toBe("fabriq")
  expect(fabriqKindFor("library")).toBe("fabriq-library")
})

test("uses name only what the manifest needs", () => {
  expect(fabriqUses(defaults, {})).toEqual({
    falkordb: { enabled: false },
    elasticsearch: { enabled: false },
  })
  expect(
    fabriqUses(
      { ...defaults, pg: "existing", redis: "existing", falkor: "new" },
      { postgres: { external: "ep_1" }, redis: { resource: "cache" } }
    )
  ).toEqual({
    postgres: { external: "ep_1" },
    redis: { resource: "cache" },
    falkordb: { enabled: true },
    elasticsearch: { enabled: false },
  })
})

const noon = new Date(2026, 9, 22, 12, 0, 0).getTime()
const before = (ms: number) => new Date(noon - ms).toISOString()
const minute = 60_000
const hour = 60 * minute

test("times read as a person would say them", () => {
  expect(ago(before(10_000), noon)).toBe("just now")
  expect(ago(before(minute), noon)).toBe("1 minute ago")
  expect(ago(before(12 * minute), noon)).toBe("12 minutes ago")
  expect(ago(before(hour), noon)).toBe("1 hour ago")
  expect(ago(before(5 * hour), noon)).toBe("5 hours ago")
  expect(ago(before(20 * hour), noon)).toBe("20 hours ago")
  expect(ago(before(30 * hour), noon)).toBe("yesterday")
  expect(ago(before(3 * 24 * hour), noon)).toBe("3 days ago")
  expect(agoDay(before(2 * hour), noon)).toBe("today")
  expect(agoDay(before(18 * hour), noon)).toBe("yesterday")
  expect(agoDay(before(3 * 24 * hour), noon)).toBe("3 days ago")
  expect(createdLabel(before(5_000), noon)).toBe("Just now")
  expect(createdLabel("2026-10-02T09:00:00Z", noon)).toMatch(/2026/)
  expect(ago("not a time", noon)).toBe("not a time")
})

test("times near a bucket edge move up instead of reading 60 minutes or 24 hours", () => {
  expect(ago(before(59 * minute), noon)).toBe("59 minutes ago")
  expect(ago(before(59.5 * minute), noon)).toBe("1 hour ago")
  expect(ago(before(89 * minute), noon)).toBe("1 hour ago")
  expect(ago(before(23 * hour), noon)).toBe("23 hours ago")
  expect(ago(before(23.4 * hour), noon)).toBe("23 hours ago")
  expect(ago(before(23.6 * hour), noon)).toBe("yesterday")
  expect(ago(before(24 * hour), noon)).toBe("yesterday")
})

test("a time earlier the same day is never yesterday", () => {
  const late = new Date(2026, 9, 22, 23, 50, 0).getTime()
  const sameDay = new Date(late - 23.6 * hour)
  expect(sameDay.getDate()).toBe(22)
  expect(ago(sameDay.toISOString(), late)).toBe("23 hours ago")
  // Once the date has changed, the same distance is yesterday.
  const early = new Date(2026, 9, 22, 0, 10, 0).getTime()
  expect(ago(new Date(early - 23.6 * hour).toISOString(), early)).toBe(
    "yesterday"
  )
})

test("services that need a Fabriq, and those that don't", () => {
  const services = {
    web: {
      needs: ["fabriq", "db"],
      need_options: { fabriq: { tenant: "globex" } },
    },
    api: { needs: ["fabriq"], need_options: { fabriq: { tenant: "acme" } } },
    bare: { needs: ["fabriq"] },
    jobs: {},
    other: {
      needs: ["fabriq-2"],
      need_options: { "fabriq-2": { tenant: "acme" } },
    },
  }
  expect(fabriqNeeds(services, "fabriq")).toEqual([
    { service: "api", tenant: "acme" },
    { service: "bare", tenant: undefined },
    { service: "web", tenant: "globex" },
  ])
  expect(unlinkedServices(services, "fabriq")).toEqual(["jobs", "other"])
  expect(fabriqNeeds(undefined, "fabriq")).toEqual([])
})

test("fabriq being down is told with its reason", () => {
  const down = Object.assign(
    new Error("Fabriq isn't reachable right now: connection refused"),
    { statusCode: 503 }
  )
  expect(unreachableReason(down)).toBe("Connection refused.")
  expect(unreachableText(unreachableReason(down))).toBe(
    "Fabriq isn't reachable right now. Connection refused."
  )
  expect(unreachableText()).toBe("Fabriq isn't reachable right now.")
  expect(
    unreachableReason(Object.assign(new Error("nope"), { statusCode: 500 }))
  ).toBeUndefined()
})

test("a running database does not imply a daily backup schedule", () => {
  const parts = stackParts({
    provider: "server",
    postgres: "managed",
    redis: "managed",
    now: Date.now(),
    falkordb: false,
    elasticsearch: false,
  })
  expect(parts.find((p) => p.id === "postgres")?.detail).not.toContain("daily")
})
