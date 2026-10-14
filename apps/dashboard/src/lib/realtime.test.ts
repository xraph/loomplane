import { afterEach, expect, it, vi } from "vitest"
import { QueryCache, type StreamConnection } from "@forge-go/client-core"
import { entities } from "@/client/forge/loomplane/entities"
import {
  useProjectsList,
  useProjectsGraph,
} from "@/client/forge/loomplane/hooks"
import { LoomplaneRealtime } from "@/lib/realtime"

afterEach(() => vi.useRealTimers())

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
