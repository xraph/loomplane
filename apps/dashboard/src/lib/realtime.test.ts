import { afterEach, expect, it, vi } from "vitest"
import { QueryCache, type StreamConnection } from "@forge-go/client-core"
import { entities } from "@/client/forge/loomplane/entities"
import {
  useCloudCapacityListExpansions,
  useCloudCapacityGetPolicy,
  useCloudCapacityUsage,
  useProjectsList,
  useProjectsGraph,
  useDeploymentsForPlan,
} from "@/client/forge/loomplane/hooks"
import { LoomplaneRealtime } from "@/lib/realtime"

afterEach(() => vi.useRealTimers())

it("refreshes a missing plan deployment when the deployment starts", async () => {
  let reads = 0
  let started = false
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async () => {
        reads++
        if (!started)
          throw Object.assign(new Error("Not started"), { status: 404 })
        return {
          id: "dep_one",
          orgId: "org_one",
          planId: "plan_one",
          status: "running",
        }
      },
    },
  })
  const query = useDeploymentsForPlan(
    { path: { id: "plan_one" } },
    { client: cache }
  )
  await query.fetch().catch(() => undefined)
  const release = query.subscribe(() => {})
  await vi.waitFor(() => expect(reads).toBeGreaterThan(1))
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  live.start()
  started = true
  connection.message({
    event: "deployment.updated",
    cursor: "org_one:1",
    data: {
      id: "dep_one",
      org_id: "org_one",
      plan_id: "plan_one",
      status: "running",
    },
  })
  await vi.waitFor(() => expect(query.getState().data?.id).toBe("dep_one"))
  release()
  live.stop()
})

it("refreshes the graph projection when analysis completes", async () => {
  let reads = 0
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async () => {
        reads++
        return {
          projectId: "proj_one",
          graph: { services: [], resources: [], edges: [] },
        }
      },
    },
  })
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  live.start()
  const graph = useProjectsGraph(
    { path: { id: "proj_one" } },
    { client: cache }
  )
  await graph.fetch()
  const release = graph.subscribe(() => {})
  connection.message({
    event: "analysis.updated",
    cursor: "org_one:1",
    data: {
      id: "analysis_one",
      org_id: "org_one",
      project_id: "proj_one",
      status: "succeeded",
    },
  })
  await vi.waitFor(() => expect(reads).toBeGreaterThan(1))
  release()
  live.stop()
})

class Connection implements StreamConnection {
  message: (frame: unknown) => void = () => {}
  dropped: () => void = () => {}
  hello: unknown[] = []
  closed = false
  onMessage(handler: (frame: unknown) => void) {
    this.message = handler
  }
  onClose(handler: () => void) {
    this.dropped = handler
  }
  send(message: unknown) {
    this.hello.push(message)
  }
  close() {
    this.closed = true
  }
}

it("restores historical collection status when a deployment joins the live scope", async () => {
  const collection = {
    id: "collection_one",
    org_id: "org_one",
    deployment_id: "dep_one",
    state: "incomplete",
    message: "Earlier build interrupted",
  }
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async () => ({
        cursor: "org_one:10",
        records: [
          { orgId: "org_one", event: "build.collection", data: collection },
        ],
      }),
    },
  })
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  const messages: unknown[] = []
  const release = live.onOutput((_event, data) => messages.push(data))
  live.start()
  connection.message({
    event: "realtime.status",
    cursor: "org_one:10",
    data: { cursor: "org_one:10" },
  })
  live.setDeployments(["dep_one"])
  await vi.waitFor(() =>
    expect(messages).toContainEqual(
      expect.objectContaining({ deploymentId: "dep_one", state: "incomplete" })
    )
  )
  release()
  live.stop()
})

it("sends scoped snapshot query parameters using their wire names", async () => {
  let query: unknown
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async (request) => {
        query = request.args.query
        return { cursor: "org_one:10", records: [] }
      },
    },
  })
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => new Connection(),
  })
  live.start()
  live.setDeployments(["dep_one"])
  await vi.waitFor(() =>
    expect(query).toEqual({ org_id: "org_one", deployment_ids: "dep_one" })
  )
  live.stop()
})

it("retries a scoped snapshot overtaken by an unrelated live event", async () => {
  let finish: (value: unknown) => void = () => {}
  let reads = 0
  const collection = {
    id: "collection_old",
    org_id: "org_one",
    deployment_id: "dep_one",
    state: "incomplete",
  }
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async () => {
        reads++
        if (reads === 1)
          return new Promise((resolve) => {
            finish = resolve
          })
        return {
          cursor: "org_one:11",
          records: [
            { orgId: "org_one", event: "build.collection", data: collection },
          ],
        }
      },
    },
  })
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  const output: unknown[] = []
  live.onOutput((_event, data) => output.push(data))
  live.start()
  connection.message({
    event: "realtime.status",
    cursor: "org_one:10",
    data: { cursor: "org_one:10" },
  })
  live.setDeployments(["dep_one"])
  await vi.waitFor(() => expect(reads).toBe(1))
  connection.message({
    event: "project.updated",
    cursor: "org_one:11",
    data: { id: "proj_other", org_id: "org_one", name: "New" },
  })
  finish({
    cursor: "org_one:10",
    records: [
      { orgId: "org_one", event: "build.collection", data: collection },
    ],
  })
  await vi.waitFor(() =>
    expect(output).toContainEqual(
      expect.objectContaining({ id: "collection_old", state: "incomplete" })
    )
  )
  expect(reads).toBe(2)
  live.stop()
})

it("prefers WS, falls back to SSE, retains replay cursor and closes old scope", async () => {
  vi.useFakeTimers()
  const connections: Connection[] = []
  const modes: string[] = []
  const cache = new QueryCache({
    entities,
    transport: { execute: async () => ({ projects: [] }) },
  })
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: (mode) => {
      modes.push(mode)
      const connection = new Connection()
      connections.push(connection)
      return connection
    },
  })
  live.start()
  expect(modes).toEqual(["ws"])
  expect(connections[0].hello).toContainEqual(
    expect.objectContaining({ org_id: "org_one" })
  )
  connections[0].message({
    event: "realtime.status",
    data: { cursor: "org_one:4" },
    cursor: "org_one:4",
  })
  expect(live.state().status).toBe("live")
  connections[0].dropped()
  expect(live.state().status).toBe("reconnecting")
  await vi.advanceTimersByTimeAsync(3000)
  expect(modes).toContain("sse")
  expect(connections.at(-1)!.hello).toContainEqual(
    expect.objectContaining({ cursor: "org_one:4" })
  )
  live.setDeployments(["dep_one"])
  expect(connections[1].closed).toBe(true)
  live.stop()
  expect(connections.at(-1)!.closed).toBe(true)
})

it("keeps a streamed entity newer than an older REST reply and ignores foreign organization data", async () => {
  let finish: (value: unknown) => void = () => {}
  let requests = 0
  const project = (name: string) => ({
    projects: [{ id: "proj_one", orgId: "org_one", name }],
  })
  const cache = new QueryCache({
    entities,
    frameRestarts: 0,
    transport: {
      execute: async () => {
        requests++
        if (requests === 1) return project("Before")
        if (requests > 2) return project("New")
        return new Promise((resolve) => {
          finish = resolve
        })
      },
    },
  })
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  live.start()
  const query = useProjectsList(undefined, { client: cache })
  await query.fetch()
  const names: (string | undefined)[] = []
  const release = query.subscribe(() => {
    names.push(query.getState().data?.projects[0].name)
  })
  const pending = query.refetch()
  await Promise.resolve()
  connection.message({
    event: "project.updated",
    cursor: "org_one:2",
    data: { id: "proj_one", org_id: "org_one", name: "New" },
  })
  finish(project("Old"))
  await pending
  expect(query.getState().data?.projects[0].name).toBe("New")
  expect(names).not.toContain("Old")
  connection.message({
    event: "project.updated",
    cursor: "org_foreign:3",
    data: { id: "proj_foreign", org_id: "org_foreign", name: "Private" },
  })
  expect(
    query.getState().data?.projects.some((p) => p.id === "proj_foreign")
  ).toBe(false)
  release()
  live.stop()
})

it("rejects a delayed snapshot and cursor rollback, and clears protected data after read denial", async () => {
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async () => {
        throw Object.assign(new Error("denied"), { statusCode: 403 })
      },
    },
  })
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  const messages: string[] = []
  live.onOutput((_, line) =>
    messages.push(
      (line as import("@/client/forge/loomplane/types").Log).message
    )
  )
  live.start()
  connection.message({
    event: "realtime.status",
    cursor: "org_one:12",
    data: { cursor: "org_one:12" },
  })
  connection.message({
    event: "realtime.status",
    cursor: "org_one:8",
    data: { cursor: "org_one:8" },
  })
  connection.message({
    event: "build.log",
    cursor: "org_one:9",
    data: {
      id: "org_one:9",
      org_id: "org_one",
      deployment_id: "dep_one",
      message: "old",
    },
  })
  expect(messages).toEqual([])
  connection.message({ event: "forge.gap", data: { from: "org_one:1" } })
  await live.synchronize()
  await vi.waitFor(() => expect(live.state().status).toBe("denied"))
  expect(connection.closed).toBe(true)
  live.stop()
})

it("refetches a Cloud's capacity policy, usage and expansions when its capacity changes", async () => {
  const reads: string[] = []
  const cache = new QueryCache({
    entities,
    transport: {
      execute: async (request) => {
        const cloud = (request.args.path as { cloudId?: string } | undefined)
          ?.cloudId
        reads.push(`${request.meta.path} ${cloud ?? ""}`)
        if (request.meta.path.endsWith("/capacity-policy"))
          return {
            cloudId: cloud,
            automaticExpansion: false,
            policy: {
              cloudId: cloud,
              orgId: "org_one",
              revision: 1,
              pools: [],
            },
          }
        if (request.meta.path.endsWith("/capacity-usage"))
          return { cloudId: cloud, accounts: [], records: [], gpus: [] }
        if (request.meta.path.endsWith("/expansions")) return { expansions: [] }
        return { cursor: "org_one:1", records: [] }
      },
    },
  })
  const queries = [
    ...["cloud_a", "cloud_b"].flatMap((cloudId) => [
      useCloudCapacityGetPolicy({ path: { cloudId } }, { client: cache }),
      useCloudCapacityUsage({ path: { cloudId } }, { client: cache }),
    ]),
    useCloudCapacityListExpansions(
      { path: { cloudId: "cloud_a" } },
      { client: cache }
    ),
  ]
  await Promise.all(queries.map((q) => q.fetch()))
  const releases = queries.map((q) => q.subscribe(() => {}))
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_one",
    connect: () => connection,
  })
  live.start()
  const count = (path: string) => reads.filter((r) => r === path).length
  const policyA = "/api/v1/clouds/{cloudId}/capacity-policy cloud_a"
  const usageA = "/api/v1/clouds/{cloudId}/capacity-usage cloud_a"
  const expansionsA = "/api/v1/clouds/{cloudId}/expansions cloud_a"
  const policyB = "/api/v1/clouds/{cloudId}/capacity-policy cloud_b"
  const usageB = "/api/v1/clouds/{cloudId}/capacity-usage cloud_b"
  expect([policyA, usageA, expansionsA, policyB, usageB].map(count)).toEqual([
    1, 1, 1, 1, 1,
  ])
  connection.message({
    event: "cloud_capacity.updated",
    cursor: "org_one:2",
    data: {
      org_id: "org_one",
      cloud_id: "cloud_a",
      kind: "expansion",
      entity_id: "exp_one",
      revision: 3,
      status: "provisioning",
    },
  })
  await vi.waitFor(() =>
    expect([policyA, usageA, expansionsA].map(count)).toEqual([2, 2, 2])
  )
  // Another Cloud's policy and usage stay as they were.
  expect([policyB, usageB].map(count)).toEqual([1, 1])
  releases.forEach((release) => release())
  live.stop()
})
