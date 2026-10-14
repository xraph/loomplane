import {
  useCallback,
  useEffect,
  useState,
  useMemo,
  useSyncExternalStore,
  useRef,
} from "react"
import { useDeploymentsLogs as deploymentLogQuery } from "@/client/forge/loomplane/hooks"
import type { Collection, Log } from "@/client/forge/loomplane/types"
import { useRealtime } from "@/lib/realtime-provider"
import { useLiveState } from "@/lib/realtime-state"

export function mergeBuildLogs(
  before: Log[],
  incoming: Log[],
  limit = 2000
): Log[] {
  const rows = new Map(before.map((line) => [line.id, line]))
  incoming.forEach((line) => rows.set(line.id, line))
  return [...rows.values()]
    .sort(
      (a, b) => Number(a.id.split(":").at(-1)) - Number(b.id.split(":").at(-1))
    )
    .slice(-limit)
}

type OutputState = {
  logs: Log[]
  collections: Collection[]
  gap: boolean
  error?: unknown
  loaded: boolean
}
function outputStore(owner: {
  deploymentId?: string
  realtime: ReturnType<typeof useRealtime>
}) {
  let snapshot: OutputState = {
    logs: [],
    collections: [],
    gap: false,
    loaded: false,
  }
  const listeners = new Set<() => void>()
  return {
    owner,
    snapshot: () => snapshot,
    subscribe: (listener: () => void) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    update: (patch: Partial<OutputState>) => {
      snapshot = { ...snapshot, ...patch }
      listeners.forEach((fn) => fn())
    },
  }
}

export function useBuildLogs(deploymentId?: string) {
  const realtime = useRealtime()
  const live = useLiveState()
  // A bounded view over immutable log records, scoped before it subscribes.
  // Entity normalization and request fencing stay in the generated Forge cache.
  const store = useMemo(
    () => outputStore({ deploymentId, realtime }),
    [deploymentId, realtime]
  )
  const output = useSyncExternalStore(
    store.subscribe,
    store.snapshot,
    store.snapshot
  )
  const [retry, setRetry] = useState(0)
  const refresh = useRef<() => Promise<void>>(async () => {})
  useEffect(() => {
    let closed = false
    if (!deploymentId) {
      store.update({ loaded: true })
      return
    }
    const unsubscribe = realtime?.onOutput((event, data) => {
      if (data.deploymentId !== deploymentId || closed) return
      const previous = store.snapshot()
      if (event === "build.log")
        store.update({ logs: mergeBuildLogs(previous.logs, [data as Log]) })
      else
        store.update({
          collections: [
            ...previous.collections.filter((row) => row.id !== data.id),
            data as Collection,
          ],
        })
    })
    realtime?.setDeployments([deploymentId])
    const read = async () => {
      try {
        let cursor = ""
        for (let page = 0; page < 10; page++) {
          const result = await deploymentLogQuery({
            path: { id: deploymentId },
            query: { cursor, limit: 1000 },
          }).refetch()
          if (closed) return
          store.update({
            logs: mergeBuildLogs(store.snapshot().logs, result.logs),
            gap: store.snapshot().gap || result.gap,
          })
          if (!result.next) break
          if (page === 9) store.update({ gap: true })
          cursor = result.next
        }
        if (!closed) store.update({ error: undefined })
      } catch (error) {
        if (!closed) store.update({ error })
      } finally {
        if (!closed) store.update({ loaded: true })
      }
    }
    refresh.current = read
    void read()
    return () => {
      closed = true
      unsubscribe?.()
      realtime?.setDeployments([])
      refresh.current = async () => {}
    }
  }, [deploymentId, realtime, retry, store])
  useEffect(() => {
    void refresh.current()
    if (live.status === "live") return
    const interval = window.setInterval(() => void refresh.current(), 15000)
    return () => window.clearInterval(interval)
  }, [live.status])
  const refetch = useCallback(() => {
    setRetry((value) => value + 1)
    return Promise.resolve()
  }, [])
  return { ...output, gap: output.gap || live.gap, refetch }
}
