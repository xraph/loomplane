import { expect, it } from "vitest"
import type { Graph, Manifest, Provider } from "@/client/forge/loomplane/types"
import {
  placementRows,
  compatibleTargets,
  placementSettings,
} from "@/lib/placement"

const graph = {
  sources: [{ alias: "app" }],
  services: [
    {
      id: "svc:app:go:cmd/api",
      source: "app",
      ecosystem: "go",
      entrypoint: "cmd/api",
      path: ".",
      kind: "http-service",
      deployable: true,
    },
    {
      id: "svc:app:go:cmd/worker",
      source: "app",
      ecosystem: "go",
      entrypoint: "cmd/worker",
      path: ".",
      kind: "worker",
      deployable: true,
    },
  ],
} as Graph
const manifest: Manifest = {
  version: 1,
  defaults: { provider: "cloud-run", region: "us-central1" },
  services: {
    api: {
      repo: "app",
      source: "go:cmd/api",
      needs: ["db"],
      environments: { preview: { provider: "ecs-fargate" } },
    },
    worker: {
      repo: "app",
      source: "go:cmd/worker",
      provider: "render",
      enabled: false,
    },
  },
  resources: { db: { kind: "postgres", provider: "cloud-sql" } },
}

it("resolves executable kinds within their source instead of treating every Go app as HTTP", () => {
  expect(placementRows(manifest, graph).map((r) => [r.name, r.kind])).toEqual([
    ["api", "http-service"],
    ["worker", "worker"],
    ["db", "postgres"],
  ])
})
it("drops a region when an environment overrides the provider and preserves disabled services", () => {
  const rows = placementRows(manifest, graph)
  expect(placementSettings(manifest, rows[0], "preview")).toEqual({
    provider: "ecs-fargate",
    region: "",
    enabled: true,
  })
  expect(placementSettings(manifest, rows[1], "production")).toEqual({
    provider: "render",
    region: "",
    enabled: false,
  })
})
it("filters targets by workload and readiness rather than cloud name", () => {
  const providers = [
    { name: "vercel", available: true, runs: [{ workload: "static-site" }] },
    {
      name: "render",
      available: true,
      runs: [{ workload: "worker" }],
      provisions: [{ kind: "postgres" }],
    },
    { name: "paused", available: false, runs: [{ workload: "worker" }] },
  ] as Provider[]
  const rows = placementRows(manifest, graph)
  expect(compatibleTargets(rows[1], providers).map((p) => p.name)).toEqual([
    "render",
  ])
  expect(compatibleTargets(rows[2], providers).map((p) => p.name)).toEqual([
    "render",
  ])
})

it("leaves ambiguous and nondeployable sources for review", () => {
  const ambiguous = {
    ...graph,
    services: [
      ...graph.services,
      { ...graph.services[0], id: "svc:app:node:cmd/api", ecosystem: "node" },
    ],
  }
  const m = {
    ...manifest,
    services: { api: { source: "cmd/api", repo: "app" } },
  }
  expect(placementRows(m, ambiguous)[0].kind).toBe("")
  expect(
    placementRows(manifest, {
      ...graph,
      services: graph.services.map((s) => ({ ...s, deployable: false })),
    })[0].kind
  ).toBe("")
})

it("honors a declared workload correction even when analysis marked the source nondeployable", () => {
  const m = {
    ...manifest,
    services: { api: { source: "go:cmd/api", repo: "app", kind: "worker" } },
  }
  expect(
    placementRows(m, {
      ...graph,
      services: graph.services.map((s) => ({ ...s, deployable: false })),
    })[0].kind
  ).toBe("worker")
})

it("offers gateway targets the deployment engine can actually build", () => {
  const row = placementRows(
    { version: 1, gateways: { edge: { routes: [] } } },
    graph
  )[0]
  const providers = ["cloud-run", "ecs-fargate", "render"].map((name) => ({
    name,
    available: true,
    runs: [{ workload: "http-service" }],
  })) as Provider[]
  expect(compatibleTargets(row, providers).map((p) => p.name)).toEqual([
    "cloud-run",
    "ecs-fargate",
  ])
})
