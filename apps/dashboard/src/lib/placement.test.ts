import { expect, it } from "vitest"
import type {
  Binding,
  Graph,
  Manifest,
  Provider,
  Target,
} from "@/client/forge/loomplane/types"
import {
  applyPlacement,
  excludedNames,
  runsOn,
  sinceLastDeploy,
  placementRows,
  placementSummary,
  compatibleTargets,
  placementSettings,
  servicesInRepo,
  withoutServices,
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

it("inherits owned targets and build context, and clears endpoints when a target changes", () => {
  const m = {
    version: 1,
    services: {
      api: {
        source: "go:cmd/api",
        provider: "server",
        target: "conn_server",
        health_path: "/ready",
        endpoint: { host_port: 8088 },
        build: {
          context: "workspace",
          builder: "conn_builder",
          registry: "conn_registry",
        },
        environments: {
          preview: {
            provider: "kubernetes",
            target: "conn_cluster",
            endpoint: { hostname: "api.test", ingress_class: "nginx" },
          },
        },
      },
    },
  } as Manifest
  const row = placementRows(m, graph)[0]
  expect(placementSettings(m, row, "production")).toMatchObject({
    target: "conn_server",
    healthPath: "/ready",
    endpoint: { hostPort: 8088 },
    build: { context: "workspace", builder: "conn_builder" },
  })
  expect(placementSettings(m, row, "preview")).toMatchObject({
    provider: "kubernetes",
    target: "conn_cluster",
    endpoint: { hostname: "api.test", ingressClass: "nginx" },
    build: { context: "workspace" },
  })
  const changed = {
    ...m,
    services: {
      api: {
        ...m.services!.api,
        environments: { preview: { target: "conn_other" } },
      },
    },
  }
  const settings = placementSettings(
    changed,
    placementRows(changed, graph)[0],
    "preview"
  )
  expect(settings.target).toBe("conn_other")
  expect(settings.endpoint).toBeUndefined()
})

it("normalizes PSC routes and follows provider and environment inheritance", () => {
  const route = {
    consumer_project: "app-project",
    network: "projects/app-project/global/networks/apps",
    subnetwork: "projects/app-project/regions/us-central1/subnetworks/apps",
  }
  const m: Manifest = {
    version: 1,
    services: {
      api: {
        source: "go:cmd/api",
        cloud_sql_network: route,
        environments: {
          production: { provider: "cloud-run" },
          preview: { provider: "render" },
          staging: {
            provider: "cloud-run",
            cloud_sql_network: {
              ...route,
              network: "projects/app-project/global/networks/staging",
            },
          },
        },
      },
    },
  }
  const row = placementRows(m, graph)[0]
  expect(placementSettings(m, row, "production").cloudSqlNetwork).toEqual({
    consumerProject: route.consumer_project,
    network: route.network,
    subnetwork: route.subnetwork,
  })
  expect(placementSettings(m, row, "preview").cloudSqlNetwork).toBeUndefined()
  expect(placementSettings(m, row, "staging").cloudSqlNetwork?.network).toBe(
    "projects/app-project/global/networks/staging"
  )
})
it("leaves out services the project excludes, so a deploy never offers them", () => {
  const worker = graph.services[1]!
  const kept = { ...graph, services: [graph.services[0]!] } as Graph
  const rows = placementRows(manifest, kept, [
    { rule: { kind: "service", match: worker.id }, service: worker },
  ])
  expect(rows.map((r) => r.name)).toEqual(["api", "db"])
  const skip = excludedNames(manifest, kept, [
    { rule: { kind: "path", match: "." }, service: worker },
  ])
  expect([...skip]).toEqual(["worker"])
  const all = {
    ...manifest,
    services: {
      ...manifest.services,
      worker: { ...manifest.services!.worker!, enabled: true },
    },
  }
  expect(placementSummary(all, "production", [], skip).services).toBe(1)
})

it("applies placement edits in memory the way the API's edit does", () => {
  const m: Manifest = {
    version: 1,
    services: {
      api: {
        source: "go:cmd/api",
        provider: "server",
        environments: {
          preview: { region: "nyc1", healthPath: "/old", cloud_tags: ["a"] },
          production: { region: "fra1" },
        },
      },
    },
  }
  const next = applyPlacement(
    m,
    { section: "services", name: "api" },
    "preview",
    {
      provider: "kubernetes",
      region: "",
      cloudTags: [],
      healthPath: "/ready",
      endpoint: { hostname: "api.example.com", hostPort: undefined },
      build: { context: "", image: "registry/api@sha256:1" },
      enabled: false,
    }
  )
  expect(next.services?.api.environments).toEqual({
    preview: {
      provider: "kubernetes",
      health_path: "/ready",
      endpoint: { hostname: "api.example.com" },
      build: { image: "registry/api@sha256:1" },
      enabled: false,
    },
    production: { region: "fra1" },
  })
  // The manifest it was given is left alone.
  expect(m.services?.api.environments.preview.region).toBe("nyc1")
  expect(() =>
    applyPlacement(m, { section: "services", name: "gone" }, "preview", {
      enabled: true,
    })
  ).toThrow("gone isn't in loomplane.yaml anymore")
})

it("names where a service runs: its Cloud target, account, tags, or off", () => {
  const catalog = [
    { name: "kubernetes", title: "Kubernetes", cloud: "kubernetes" },
    { name: "do-apps", title: "App Platform", cloud: "digitalocean" },
  ] as Provider[]
  const targets = [
    {
      name: "backend",
      displayName: "backend target",
      provider: "do-apps",
      region: "nyc1",
    },
  ] as Target[]
  const connections = [{ id: "c1", name: "Prod cluster" }] as never[]
  const base = { provider: "", region: "", enabled: true }
  expect(
    runsOn(
      { ...base, cloudTarget: "backend" },
      "production",
      catalog,
      targets,
      connections
    )
  ).toEqual({
    cloud: "digitalocean",
    name: "backend target",
    where: "App Platform · nyc1",
  })
  expect(
    runsOn(
      { ...base, provider: "kubernetes", target: "c1" },
      "production",
      catalog,
      targets,
      connections
    )
  ).toEqual({
    cloud: "kubernetes",
    name: "Prod cluster",
    where: "Kubernetes · automatic region",
  })
  expect(
    runsOn(
      { ...base, cloudTags: ["api", "eu"] },
      "production",
      catalog,
      targets,
      connections
    ).name
  ).toBe("Target tagged api, eu")
  expect(
    runsOn(
      { ...base, enabled: false },
      "production",
      catalog,
      targets,
      connections
    ).name
  ).toBe("Off in production")
  expect(runsOn(base, "production", catalog, targets, connections).name).toBe(
    "Not placed yet"
  )
})

it("ticks a service that's new, drifted or behind its repository", () => {
  const [row] = placementRows(manifest, graph)
  const g = { ...graph, sources: [{ alias: "app", commitSha: "new" }] } as Graph
  const bound = (b: Partial<Binding>) =>
    ({ commitSha: "new", drift: "", ...b }) as Binding
  expect(sinceLastDeploy(row, g, undefined, true)).toMatchObject({
    label: "New here",
    changed: true,
  })
  expect(
    sinceLastDeploy(row, g, bound({ commitSha: "old" }), true)
  ).toMatchObject({ label: "New commits", changed: true })
  expect(
    sinceLastDeploy(row, g, bound({ drift: "changed" }), true)
  ).toMatchObject({ label: "Drifted", changed: true })
  expect(sinceLastDeploy(row, g, bound({}), true)).toMatchObject({
    label: "No changes",
    changed: false,
  })
  expect(sinceLastDeploy(row, g, undefined, false)).toMatchObject({
    label: "Turned off",
    changed: false,
  })
})

it("names each service's repository and says when it was removed", () => {
  const rows = placementRows(manifest, graph)
  expect(rows[0]).toMatchObject({ name: "api", repo: "app", repoGone: false })
  const removed = { ...graph, sources: [], services: [] } as Graph
  expect(placementRows(manifest, removed)[0]).toMatchObject({
    name: "api",
    repo: "app",
    repoGone: true,
    kind: "",
  })
  // A one-repository project's services can leave repo out.
  const bare: Manifest = {
    version: 1,
    services: { api: { source: "go:cmd/api" } },
  }
  expect(placementRows(bare, graph)[0]).toMatchObject({
    repo: "app",
    repoGone: false,
  })
  expect(placementRows(bare, removed)[0]).toMatchObject({
    repo: "",
    repoGone: true,
  })
  // A graph from before repositories has none, and matches anywhere, as
  // the planner's does.
  const before = { ...graph, sources: [] } as Graph
  expect(placementRows(bare, before)[0]).toMatchObject({ repoGone: false })
  // With two repositories, one without repo is ambiguous, not removed.
  const two = { ...graph, sources: [{ alias: "app" }, { alias: "web" }] }
  expect(placementRows(bare, two as Graph)[0]).toMatchObject({
    repoGone: false,
  })
})

it("finds the services a repository holds", () => {
  expect(servicesInRepo(manifest, graph, "app")).toEqual(["api", "worker"])
  expect(servicesInRepo(manifest, graph, "web")).toEqual([])
  const bare: Manifest = {
    version: 1,
    services: { api: { source: "go:cmd/api" } },
  }
  expect(servicesInRepo(bare, graph, "app")).toEqual(["api"])
})

it("drops services with every need and route that names them", () => {
  const m: Manifest = {
    version: 1,
    services: {
      api: { repo: "app", source: "go:cmd/api" },
      web: {
        repo: "web",
        source: "apps/web",
        needs: ["api", "db"],
        need_options: { api: { path: "/v1" } },
      },
    },
    resources: { db: { kind: "postgres" } },
    gateways: {
      edge: {
        routes: [
          { path: "/api", to: "api" },
          { path: "/", to: "web" },
        ],
      },
      internal: { routes: [{ path: "/", to: "api" }] },
    },
  }
  const { manifest: next, gateways } = withoutServices(m, ["api"])
  expect(Object.keys(next.services!)).toEqual(["web"])
  expect(next.services!.web).toMatchObject({ needs: ["db"] })
  expect(next.services!.web.need_options).toBeUndefined()
  expect(next.gateways!.edge.routes).toEqual([{ path: "/", to: "web" }])
  // A gateway left with no route can't be saved, so it goes too.
  expect(next.gateways!.internal).toBeUndefined()
  expect(gateways).toEqual(["internal"])
  // The original isn't touched.
  expect(Object.keys(m.services!)).toEqual(["api", "web"])
})
