import { describe, expect, it } from "vitest"

import type { Analysis, Graph } from "@/client/forge/loomplane/types"
import {
  connections,
  evidenceLabel,
  evidenceURL,
  isActive,
  kindLabel,
  latestBySource,
  latestSucceeded,
  stepIndex,
} from "@/lib/architecture"

const sha = "0123456789abcdef0123456789abcdef01234567"

describe("evidenceURL", () => {
  it("links to the line at the exact commit", () => {
    expect(
      evidenceURL("acme/mono", sha, {
        path: "services/api/main.go",
        line: 13,
        detail: "",
      })
    ).toBe(`https://github.com/acme/mono/blob/${sha}/services/api/main.go#L13`)
  })

  it("escapes each path segment and drops a missing line", () => {
    expect(
      evidenceURL("acme/mono", sha, { path: "apps/my app/#x.json", detail: "" })
    ).toBe(`https://github.com/acme/mono/blob/${sha}/apps/my%20app/%23x.json`)
  })
})

describe("evidenceLabel", () => {
  it("adds the line when there is one", () => {
    expect(evidenceLabel({ path: "go.mod", line: 3, detail: "" })).toBe(
      "go.mod:3"
    )
    expect(evidenceLabel({ path: "go.mod", detail: "" })).toBe("go.mod")
  })
})

describe("analysis status", () => {
  it.each([
    ["queued", true, 0],
    ["fetching", true, 1],
    ["analyzing", true, 2],
    ["succeeded", false, -1],
    ["failed", false, -1],
  ])("%s: active %s, step %i", (status, active, step) => {
    expect(isActive(status)).toBe(active)
    expect(stepIndex(status)).toBe(step)
  })
})

describe("kindLabel", () => {
  it("names known kinds and passes others through", () => {
    expect(kindLabel("web-app")).toBe("Web app")
    expect(kindLabel("lambda")).toBe("lambda")
  })
})

describe("connections", () => {
  const graph = {
    services: [],
    packages: [
      {
        id: "pkg:go:lib",
        name: "lib",
        ecosystem: "go",
        path: "lib",
        evidence: [],
      },
    ],
    resources: [
      {
        id: "res:postgres",
        kind: "postgres",
        confidence: "likely",
        needsConfirmation: false,
        evidence: [],
      },
    ],
    edges: [
      {
        from: "svc:go:api",
        to: "pkg:go:lib",
        kind: "uses",
        confidence: "certain",
        evidence: [],
      },
      {
        from: "svc:go:api",
        to: "res:postgres",
        kind: "needs",
        confidence: "possible",
        evidence: [],
      },
      {
        from: "svc:go:other",
        to: "res:postgres",
        kind: "needs",
        confidence: "likely",
        evidence: [],
      },
    ],
    workspaces: [],
    buildSystems: [],
    notes: [],
    version: 1,
  } satisfies Graph

  it("lists what one service uses and needs, with the edge's confidence", () => {
    const c = connections(graph, "svc:go:api")
    expect(c.uses.map((p) => p.name)).toEqual(["lib"])
    expect(c.needs).toEqual([
      { resource: graph.resources[0], confidence: "possible" },
    ])
  })

  it("follows packages to what they need, however deep", () => {
    const deep: Graph = {
      ...graph,
      packages: [
        ...graph.packages,
        {
          id: "pkg:node:db",
          name: "@acme/db",
          ecosystem: "node",
          path: "packages/db",
          evidence: [],
        },
      ],
      resources: [
        ...graph.resources,
        {
          id: "res:redis",
          kind: "redis",
          confidence: "likely",
          needsConfirmation: false,
          evidence: [],
        },
      ],
      edges: [
        {
          from: "svc:node:web",
          to: "pkg:go:lib",
          kind: "uses",
          confidence: "certain",
          evidence: [],
        },
        {
          from: "pkg:go:lib",
          to: "pkg:node:db",
          kind: "uses",
          confidence: "certain",
          evidence: [],
        },
        {
          from: "pkg:node:db",
          to: "res:postgres",
          kind: "needs",
          confidence: "likely",
          evidence: [],
        },
        {
          from: "pkg:node:db",
          to: "pkg:go:lib",
          kind: "uses",
          confidence: "certain",
          evidence: [],
        },
        {
          from: "svc:node:web",
          to: "res:redis",
          kind: "needs",
          confidence: "possible",
          evidence: [],
        },
      ],
    }
    const c = connections(deep, "svc:node:web")
    expect(c.uses.map((p) => p.name)).toEqual(["lib"])
    expect(c.needs.map((n) => [n.resource.kind, n.via])).toEqual([
      ["redis", undefined],
      ["postgres", "@acme/db"],
    ])
  })
})

describe("latestSucceeded", () => {
  const a = (id: string, status: string) => ({ id, status }) as Analysis
  it("skips running and failed analyses", () => {
    expect(
      latestSucceeded([a("3", "queued"), a("2", "failed"), a("1", "succeeded")])
        ?.id
    ).toBe("1")
    expect(latestSucceeded([a("1", "failed")])).toBeUndefined()
  })
})

describe("latestBySource", () => {
  it("keeps each source's newest analysis from a list newest first", () => {
    const list = [
      { id: "3", source: "web", status: "queued" },
      { id: "2", source: "api", status: "succeeded" },
      { id: "1", source: "web", status: "succeeded" },
    ] as Analysis[]
    const got = latestBySource(list)
    expect([...got.entries()].map(([k, a]) => `${k}:${a.id}`)).toEqual([
      "web:3",
      "api:2",
    ])
  })
})
