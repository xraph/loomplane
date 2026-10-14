import {
  applyFrames,
  channelMessages,
  eventSourceConnection,
  forgeStreamingDecoder,
  StreamBinder,
  SubscriptionManager,
  type QueryCache,
  type StreamBinding,
  type StreamConnection,
} from "@forge-go/client-core"
import { decode, CODECS } from "@/client/forge/loomplane/codecs"
import {
  useRealtimeSnapshot as snapshotQuery,
  useInfrastructureGet as infrastructureQuery,
} from "@/client/forge/loomplane/hooks"
import { op_realtime_events } from "@/client/forge/loomplane/ops/realtime.events"
import { op_realtime_ws } from "@/client/forge/loomplane/ops/realtime.ws"
import { streams } from "@/client/forge/loomplane/stream-bindings"
import type { Collection, Log, Snapshot } from "@/client/forge/loomplane/types"
import { RealtimeWsReceiveWSClient } from "@/client/forge/loomplane/websocket"
import { statusOf } from "@/client/errors"
import type { LiveState } from "@/lib/realtime-state"

type Scope = { orgId: string; deploymentIds: string[]; cursor: string }
type Frame = { event?: string; data?: unknown; cursor?: string; id?: string }
type Options = {
  cache: QueryCache
  orgId: string
  connect?: (mode: "ws" | "sse", scope: Scope) => StreamConnection
}
const channel = op_realtime_ws.path
const bindings: readonly StreamBinding[] = streams.filter(
  (b) => b.channel === channel
)
const decoder = forgeStreamingDecoder()

function connectNative(mode: "ws" | "sse", scope: Scope): StreamConnection {
  if (mode === "ws") {
    const client = new RealtimeWsReceiveWSClient({
      baseURL: window.location.origin,
      connectionTimeout: 5000,
      enableOfflineQueue: false,
    })
    return {
      onMessage: (fn) => client.onMessage(fn),
      onClose: (fn) => client.onClose(fn),
      onError: (fn) => client.onError(fn),
      onOpen: (fn) => {
        client
          .connect()
          .then(fn)
          .catch(() => undefined)
      },
      send: (value) => {
        void client
          .send(value as Parameters<typeof client.send>[0])
          .catch(() => undefined)
      },
      close: () => client.close(true),
    }
  }
  const url = new URL(op_realtime_events.path, window.location.origin)
  url.searchParams.set("org_id", scope.orgId)
  if (scope.deploymentIds.length)
    url.searchParams.set("deployment_ids", scope.deploymentIds.join(","))
  if (scope.cursor) url.searchParams.set("cursor", scope.cursor)
  const source = new EventSource(url, { withCredentials: true })
  const connection = eventSourceConnection(
    {
      addEventListener: (name, listener) =>
        source.addEventListener(name, (event) =>
          listener(event as MessageEvent<string>)
        ),
      get onerror(): ((event: unknown) => void) | null {
        return null
      },
      set onerror(handler) {
        source.onerror = (event) => handler?.(event)
      },
      close: () => source.close(),
    },
    {
      events: [
        ...channelMessages(streams, op_realtime_events.path),
        "realtime.snapshot",
        "realtime.status",
      ],
    }
  )
  return { ...connection, onOpen: (fn) => source.addEventListener("open", fn) }
}

/** One Forge-managed connection for this organization and visible log scope. */
export class LoomplaneRealtime {
  private manager?: SubscriptionManager
  private binder?: StreamBinder
  private releases: (() => void)[] = []
  private timer?: ReturnType<typeof setInterval>
  private cursor = ""
  private deployments: string[] = []
  private mode: "ws" | "sse" = "ws"
  private snapshot: LiveState = {
    status: "reconnecting",
    transport: "ws",
    lastSync: 0,
    gap: false,
  }
  private listeners = new Set<() => void>()
  private frames = new Set<(event: string, data: Log | Collection) => void>()
  private stopped = true
  private generation = 0
  private syncingGeneration = -1
  constructor(private options: Options) {}
  state = () => this.snapshot
  subscribe = (fn: () => void) => {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }
  onOutput = (fn: (event: string, data: Log | Collection) => void) => {
    this.frames.add(fn)
    return () => this.frames.delete(fn)
  }
  private update(patch: Partial<LiveState>) {
    this.snapshot = { ...this.snapshot, ...patch }
    this.listeners.forEach((fn) => fn())
  }
  start() {
    this.stopped = false
    this.open()
    this.timer = setInterval(() => {
      if (this.snapshot.lastSync && Date.now() - this.snapshot.lastSync > 10000)
        this.update({ status: "stale" })
      if (this.snapshot.status !== "live") void this.synchronize()
    }, 15000)
  }
  stop() {
    this.stopped = true
    this.generation++
    if (this.timer) clearInterval(this.timer)
    this.close()
    this.frames.clear()
    this.update({ status: "stale" })
  }
  setDeployments(ids: string[]) {
    const next = [...new Set(ids)].sort().slice(0, 64)
    if (next.join(",") === this.deployments.join(",")) return
    this.deployments = next
    if (!this.stopped) {
      this.close()
      this.open()
      void this.synchronize()
    }
  }
  private close() {
    this.generation++
    this.releases.forEach((fn) => fn())
    this.releases = []
    this.binder?.dispose()
    this.manager?.closeAll()
  }
  private open() {
    const generation = ++this.generation
    this.update({ status: "reconnecting", transport: this.mode })
    this.manager = new SubscriptionManager({
      principal: () => this.options.cache.owner,
      endpointOf: () => channel,
      random: () => 0.5,
      backoff: { attempts: Infinity, baseDelay: 500, maxDelay: 15000 },
      connect: () => {
        const scope = {
          orgId: this.options.orgId,
          deploymentIds: this.deployments,
          cursor: this.cursor,
        }
        const mode = this.mode
        const connection = (this.options.connect ?? connectNative)(mode, scope)
        return {
          close: () => connection.close(),
          // SSE scope travels in its authenticated URL; only WS sends hello.
          send: (value) => connection.send?.(value),
          onOpen: (fn) => {
            if (connection.onOpen) connection.onOpen(fn)
            else fn()
          },
          onMessage: (fn) =>
            connection.onMessage((raw) => {
              if (generation === this.generation && !this.stopped)
                this.receive(raw, fn)
            }),
          onClose: (fn) =>
            connection.onClose((reason) => {
              if (generation !== this.generation || this.stopped) return
              this.mode = "sse"
              this.update({ status: "reconnecting", transport: this.mode })
              void this.synchronize()
              fn(reason)
            }),
          onError: (fn) =>
            connection.onError?.((error) => {
              if (generation === this.generation)
                this.update({ status: "reconnecting" })
              fn(error)
            }),
        }
      },
    })
    this.binder = new StreamBinder({
      cache: this.options.cache,
      streams: bindings,
      manager: this.manager,
      scheduler: (fn) => fn(),
      decode: (frame) => {
        const decoded = decoder(frame)
        return decoded ? { ...decoded, channel } : undefined
      },
      onUnknown: () => undefined,
    })
    this.releases = [
      this.binder.channel(channel),
      this.manager.subscribe(channel, () => {}, {
        hello: () => ({
          op: "subscribe",
          org_id: this.options.orgId,
          deployment_ids: this.deployments,
          cursor: this.cursor,
        }),
      }),
    ]
  }
  private sequence(value: string) {
    if (!value.startsWith(this.options.orgId + ":")) return undefined
    try {
      return BigInt(value.slice(this.options.orgId.length + 1))
    } catch {
      return undefined
    }
  }
  private output(event: string, data: unknown) {
    if (event !== "build.log" && event !== "build.collection") return
    const binding = bindings.find(
      (b) => b.kind === "entity" && b.message === event
    )
    if (!binding || binding.kind !== "entity") return
    const decoded = binding.decode?.(data) as Log | Collection
    if (decoded?.orgId === this.options.orgId)
      this.frames.forEach((fn) => fn(event, decoded))
  }
  private receive(raw: unknown, deliver: (frame: unknown) => void) {
    if (typeof raw !== "object" || raw === null) return
    const frame = raw as Frame
    const cursor = frame.cursor ?? frame.id ?? ""
    const position = cursor ? this.sequence(cursor) : undefined
    if (cursor && position === undefined) return
    if (frame.event === "realtime.snapshot") {
      const snapshot = decode(frame.data, CODECS.Snapshot) as Snapshot
      this.applySnapshot(snapshot, deliver)
      return
    }
    if (frame.event === "forge.gap") {
      this.update({ gap: true, status: "reconnecting" })
      void this.synchronize()
    } else if (frame.event === "forge.resumed") {
      if (
        cursor &&
        (this.sequence(this.cursor) ?? -1n) <= this.sequence(cursor)!
      )
        this.cursor = cursor
    } else if (frame.event === "realtime.status") {
      const status = frame.data as { cursor?: string }
      if (
        !cursor &&
        status.cursor &&
        this.sequence(status.cursor) === undefined
      )
        return
      const next = cursor || status.cursor || this.cursor
      if ((this.sequence(next) ?? -1n) >= (this.sequence(this.cursor) ?? -1n))
        this.cursor = next
      this.update({
        status: "live",
        lastSync: Date.now(),
        transport: this.mode,
      })
    } else {
      const old = this.cursor ? this.sequence(this.cursor) : undefined
      if (position !== undefined && old !== undefined && position <= old) return
      const data = frame.data as
        | {
            org_id?: string
            orgId?: string
            project_id?: string
            projectId?: string
            plan_id?: string
            planId?: string
          }
        | undefined
      if (
        data &&
        (data.org_id ?? data.orgId) &&
        (data.org_id ?? data.orgId) !== this.options.orgId
      )
        return
      if (cursor) this.cursor = cursor
      this.output(frame.event ?? "", frame.data)
      this.refreshDeploymentLookup(frame.event, frame.data)
      if (
        frame.event === "analysis.updated" ||
        frame.event === "manifest.updated"
      ) {
        const project = data?.project_id ?? data?.projectId
        if (project) this.options.cache.invalidate([`ProjectView:${project}`])
      }
      if (
        frame.event === "binding.updated" ||
        frame.event === "binding.deleted" ||
        frame.event === "connection.updated" ||
        frame.event === "connection.deleted"
      )
        void infrastructureQuery(undefined, { client: this.options.cache })
          .refetch()
          .catch(() => undefined)
    }
    deliver(frame)
  }
  private refreshDeploymentLookup(event: string | undefined, data: unknown) {
    if (event !== "deployment.updated" || !data) return
    const deployment = data as { plan_id?: string; planId?: string }
    const plan = deployment.plan_id ?? deployment.planId
    if (plan) this.options.cache.invalidate([`Deployment:${plan}`])
  }
  private applySnapshot(
    snapshot: Snapshot,
    deliver?: (frame: unknown) => void
  ) {
    if (!snapshot.cursor || this.sequence(snapshot.cursor) === undefined) return
    const previous = this.cursor ? this.sequence(this.cursor) : undefined
    if (previous !== undefined && this.sequence(snapshot.cursor)! < previous)
      return
    const records = snapshot.records ?? []
    for (const record of records) {
      if (record.orgId !== this.options.orgId) continue
      this.refreshDeploymentLookup(record.event, record.data)
      this.output(record.event, record.data)
      const frame = { event: record.event, data: record.data, channel }
      if (deliver) deliver(frame)
      else {
        const binding = bindings.find(
          (b) => b.kind === "entity" && b.message === record.event
        )
        if (binding?.kind === "entity")
          applyFrames(this.options.cache, [{ binding, payload: record.data }])
      }
    }
    this.cursor = snapshot.cursor
    if (snapshot.partial)
      this.options.cache.invalidate(
        bindings.flatMap((b) =>
          b.kind === "entity" ? [...(b.invalidates ?? [])] : []
        )
      )
    this.update({ lastSync: Date.now() })
  }
  async synchronize() {
    if (
      this.syncingGeneration === this.generation ||
      this.stopped ||
      this.snapshot.status === "denied"
    )
      return
    const generation = this.generation
    this.syncingGeneration = generation
    try {
      const result = await snapshotQuery(
        {
          query: {
            orgId: this.options.orgId,
            deploymentIds: this.deployments.join(","),
          },
        },
        { client: this.options.cache }
      ).refetch()
      if (generation === this.generation && !this.stopped)
        this.applySnapshot(result)
    } catch (error) {
      if (generation === this.generation && !this.stopped) {
        if (statusOf(error) === 401 || statusOf(error) === 403) {
          this.close()
          this.options.cache.clear()
          this.frames.clear()
          this.update({ status: "denied" })
        } else this.update({ status: "stale" })
      }
    } finally {
      if (this.syncingGeneration === generation) this.syncingGeneration = -1
    }
  }
}
