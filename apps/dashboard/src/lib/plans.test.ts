import { describe, expect, it } from "vitest"

import type { Action, Manifest, Provider } from "@/client/forge/loomplane/types"
import {
  planStatus,
  scopeBody,
  steps,
  withIgnored,
  withResource,
} from "@/lib/plans"

const manifest: Manifest = {
  version: 1,
  defaults: {},
  ignore: {},
  services: {
    api: { source: "services/api", provider: "cloud-run" },
    web: { source: "apps/web", provider: "vercel" },
    gateway: { source: "apps/gateway", provider: "cloud-run" },
  },
}

const provider = (name: string, cloud: string, kinds: string[]): Provider => ({
  name,
  cloud,
  title: name,
  description: "",
  model: "managed",
  scaling: false,
  available: false,
  provisions: kinds.map((kind) => ({ kind, creates: kind })),
})

const providers = [
  provider("cloud-run", "gcp", []),
  provider("vercel", "vercel", []),
  provider("render", "render", ["postgres", "redis"]),
  provider("cloud-sql", "gcp", ["postgres", "mysql"]),
  provider("rds", "aws", ["postgres", "mysql"]),
]

describe("withResource", () => {
  it("provisions on the cloud most services run on", () => {
    const m = withResource(manifest, "mysql", providers)
    expect(m.resources).toEqual({
      mysql: { kind: "mysql", provider: "cloud-sql" },
    })
    // The manifest it was given is left alone.
    expect(manifest.resources).toBeUndefined()
  })

  it("numbers a name that's taken", () => {
    const m = withResource(
      { ...manifest, resources: { mysql: { kind: "mysql" } } },
      "mysql",
      providers
    )
    expect(Object.keys(m.resources ?? {})).toEqual(["mysql", "mysql-2"])
  })

  it("leaves the provider out when nothing provisions the kind", () => {
    const m = withResource(manifest, "kafka", providers)
    expect(m.resources).toEqual({ kafka: { kind: "kafka" } })
  })
})

describe("scopeBody", () => {
  it("sends no scope when every repository moves", () => {
    expect(scopeBody(["api", "web"], ["web", "api"])).toEqual({})
  })
  it("names the repositories that move otherwise", () => {
    expect(scopeBody(["api", "web"], ["web"])).toEqual({ scope: ["web"] })
  })
})

describe("withIgnored", () => {
  it("adds a kind once", () => {
    const once = withIgnored(manifest, "mysql")
    expect(once.ignore.resources).toEqual(["mysql"])
    expect(withIgnored(once, "mysql")).toBe(once)
  })
})

describe("steps", () => {
  it("groups actions by step, in order, by name within one", () => {
    const a = (name: string, step: number) => ({ name, step }) as Action
    const got = steps([a("web", 3), a("db", 1), a("api", 2), a("cache", 1)])
    expect(got.map((s) => s.map((x) => x.name))).toEqual([
      ["cache", "db"],
      ["api"],
      ["web"],
    ])
  })
})

describe("planStatus", () => {
  it("names every status the API sends", () => {
    for (const s of [
      "planning",
      "ready",
      "blocked",
      "approved",
      "rejected",
      "expired",
      "superseded",
      "stale",
      "failed",
    ]) {
      expect(planStatus(s).label).not.toBe(s)
    }
  })
})
