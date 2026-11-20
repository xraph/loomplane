import { describe, expect, it } from "vitest"

import type { Connection, Provider } from "@/client/forge/loomplane/types"

import {
  entries,
  envVarFor,
  nameFromSource,
  nameProblem,
  placementsFor,
  runnersOf,
  scopeConflicts,
  slug,
} from "./add-service"

describe("names", () => {
  it("names a source for its directory, process or Dockerfile's directory", () => {
    expect(nameFromSource("go:services/api")).toBe("api")
    expect(nameFromSource("apps/web/")).toBe("web")
    expect(nameFromSource("apps/web/Dockerfile")).toBe("web")
    expect(nameFromSource("Dockerfile")).toBe("app")
    expect(nameFromSource("shop/Procfile/worker")).toBe("worker")
    expect(nameFromSource("services/ML_Model")).toBe("ml-model")
    expect(nameFromSource("")).toBe("")
    expect(slug("2fa")).toBe("service-2fa")
  })

  it("says what's wrong with a name, and nothing about an empty one", () => {
    const taken = new Set(["api"])
    expect(nameProblem("", taken)).toBe("")
    expect(nameProblem("worker", taken)).toBe("")
    expect(nameProblem("api", taken)).toMatch(/already has something/)
    expect(nameProblem("Worker", taken)).toMatch(/lower case/)
    expect(nameProblem("a--b", taken)).toMatch(/single hyphens/)
  })
})

describe("envVarFor", () => {
  it("names a resource's variable by its kind, a service's by its name", () => {
    expect(envVarFor({ name: "db", kind: "postgres" }, "resources")).toBe(
      "DATABASE_URL"
    )
    expect(envVarFor({ name: "cache", kind: "valkey" }, "resources")).toBe(
      "REDIS_URL"
    )
    expect(envVarFor({ name: "gw", kind: "sample" }, "resources")).toBe(
      "GW_URL"
    )
    expect(envVarFor({ name: "billing-api" }, "services")).toBe(
      "BILLING_API_URL"
    )
  })
})

describe("scopeConflicts", () => {
  const manifest = {
    version: 1,
    services: {
      api: { source: "cmd/api" },
      admin: {
        source: "cmd/admin",
        environments: { production: { enabled: false } },
      },
    },
    resources: { db: { kind: "postgres" } },
  }
  const envs = ["production", "staging"]

  it("allows anything added to every environment", () => {
    expect(
      scopeConflicts(manifest, envs, {
        name: "cache",
        environment: "staging",
        only: false,
        neededBy: ["api"],
      })
    ).toEqual([])
  })

  it("names a service that runs where its new need wouldn't", () => {
    expect(
      scopeConflicts(manifest, envs, {
        name: "cache",
        environment: "staging",
        only: true,
        neededBy: ["api", "admin"],
      })
    ).toEqual([
      "api runs in production and would need cache, which would be off there.",
    ])
  })

  it("names a need that's off where the new service runs", () => {
    expect(
      scopeConflicts(manifest, envs, {
        name: "worker",
        environment: "production",
        only: true,
        needs: ["admin", "db"],
      })
    ).toEqual(["worker needs admin, which is off in production."])
  })
})

describe("placementsFor", () => {
  const connections = [
    {
      id: "c1",
      cloud: "server",
      name: "box",
      status: "ready",
      details: { architecture: "amd64" },
    },
    {
      id: "c2",
      cloud: "server",
      name: "down",
      status: "failed",
      details: { architecture: "amd64" },
    },
  ] as unknown as Connection[]
  const providers = [
    {
      name: "cloud-sql",
      cloud: "gcp",
      title: "Cloud SQL",
      available: true,
      provisions: [{ kind: "postgres", creates: "instance" }],
    },
    {
      name: "rds",
      cloud: "aws",
      title: "RDS",
      available: false,
      provisions: [{ kind: "postgres", creates: "instance" }],
    },
    {
      name: "memorystore",
      cloud: "gcp",
      title: "Memorystore",
      available: true,
      provisions: [{ kind: "redis", creates: "instance" }],
    },
  ] as unknown as Provider[]

  it("offers ready targets first, then available providers that provision it", () => {
    const got = placementsFor(
      { name: "postgres", targets: ["server", "kubernetes"] },
      connections,
      providers
    )
    expect(got.map((p) => p.key)).toEqual(["server:c1", "cloud-sql"])
    expect(got[0]).toMatchObject({ provider: "server", target: "c1" })
    expect(got[1].target).toBeUndefined()
  })

  it("lists the providers that run a kind of service", () => {
    const list = [
      { name: "vercel", available: true, runs: [{ workload: "web-app" }] },
      { name: "ecs", available: true, runs: [{ workload: "worker" }] },
      { name: "off", available: false, runs: [{ workload: "worker" }] },
    ] as unknown as Provider[]
    expect(runnersOf(list, "worker").map((p) => p.name)).toEqual(["ecs"])
    expect(runnersOf(list).map((p) => p.name)).toEqual(["vercel", "ecs"])
  })
})

it("lists the manifest's entries by name", () => {
  expect(
    entries({
      version: 1,
      services: { web: { source: "apps/web" } },
      resources: { db: { kind: "postgres" } },
    })
  ).toEqual([
    { name: "db", section: "resources", kind: "postgres" },
    { name: "web", section: "services", kind: undefined },
  ])
})
