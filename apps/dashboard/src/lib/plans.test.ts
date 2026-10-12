import { describe, expect, it } from "vitest"

import type { Action, Manifest, Provider } from "@/client/forge/loomplane/types"
import {
  chosenScope,
  lockedOn,
  planStatus,
  scopeBody,
  steps,
  toggleScope,
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
  // The API reads an empty scope as every repository, which is why the
  // picker won't plan with none chosen.
  it("names an empty scope when nothing moves", () => {
    expect(scopeBody(["api", "web"], [])).toEqual({ scope: [] })
  })
  it("sends no scope for a project without repositories", () => {
    expect(scopeBody([], [])).toEqual({})
  })
})

describe("toggleScope", () => {
  it("holds and moves a repository", () => {
    expect(toggleScope(["api", "web"], ["api", "web"], "web")).toEqual(["api"])
    expect(toggleScope(["api", "web", "db"], ["api"], "web")).toEqual([
      "api",
      "web",
    ])
  })
  it("goes back to every repository once all of them move", () => {
    expect(toggleScope(["api", "web"], ["api"], "web")).toBeNull()
  })
  it("won't hold a repository that's locked on", () => {
    const locked = new Set(["web"])
    expect(
      toggleScope(["api", "web"], ["api", "web"], "web", locked)
    ).toBeNull()
    expect(
      toggleScope(["api", "web", "db"], ["api", "web"], "web", locked)
    ).toEqual(["api", "web"])
    // The rest still toggle.
    expect(toggleScope(["api", "web"], ["api", "web"], "api", locked)).toEqual([
      "web",
    ])
  })
})

describe("lockedOn", () => {
  it("locks on each repository with nothing deployed", () => {
    expect(lockedOn(["api", "web", "db"], ["api"])).toEqual(
      new Set(["web", "db"])
    )
  })
  it("locks nothing once everything is deployed", () => {
    expect(lockedOn(["api", "web"], ["web", "api", "gone"]).size).toBe(0)
  })
})

describe("chosenScope", () => {
  it("is every repository when the picker holds none back", () => {
    expect(chosenScope(["api", "web"], null, new Set())).toEqual(["api", "web"])
  })
  it("keeps a locked repository in, even one the picker held", () => {
    expect(chosenScope(["api", "web", "db"], ["api"], new Set(["db"]))).toEqual(
      ["api", "db"]
    )
  })
  it("plans everything when nothing is deployed", () => {
    const all = ["api", "web"]
    const chosen = chosenScope(all, [], lockedOn(all, []))
    expect(scopeBody(all, chosen)).toEqual({})
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
