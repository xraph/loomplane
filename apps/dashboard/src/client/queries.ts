// useQueries follows one query binding with many arguments, such as every
// project's deployments. The generated client's useQuery takes one set of
// arguments, and hooks can't be called in a loop, so this mounts a handle
// per set the same way useQuery does and reads them as one snapshot.
import type {
  QueryBinding,
  QueryState,
  TagContext,
} from "@forge-go/client-core"
import { useClient } from "@forge-go/client-react"
import { useCallback, useMemo, useRef, useSyncExternalStore } from "react"

export interface QueriesResult<T> {
  /** One state per set of arguments, in the same order. */
  states: QueryState<T>[]
  /** refetch runs every query again. */
  refetch: () => Promise<unknown>
}

export function useQueries<T>(
  op: QueryBinding<T>,
  argsList: TagContext[]
): QueriesResult<T> {
  const client = useClient()
  // The arguments as a key, so a new array with the same contents doesn't
  // remount every handle.
  const key = argsList.map((a) => client.key(op.meta, a)).join("\n")
  const handles = useMemo(
    () => argsList.map((a) => op(a, { client })),
    // argsList is read through key: the same contents keep the handles.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [op, client, key]
  )

  const subscribe = useCallback(
    (listener: () => void) => {
      const stops = handles.map((h) => h.subscribe(listener))
      return () => stops.forEach((stop) => stop())
    },
    [handles]
  )

  // useSyncExternalStore needs the same array back while nothing changed.
  const last = useRef<QueryState<T>[]>([])
  const read = useCallback(
    (server: boolean) => {
      const next = handles.map((h) =>
        server ? h.getServerState() : h.getState()
      )
      const prev = last.current
      if (prev.length === next.length && next.every((s, i) => s === prev[i])) {
        return prev
      }
      last.current = next
      return next
    },
    [handles]
  )
  const getState = useCallback(() => read(false), [read])
  const getServerState = useCallback(() => read(true), [read])
  const states = useSyncExternalStore(subscribe, getState, getServerState)

  const refetch = useCallback(
    () => Promise.allSettled(handles.map((h) => h.refetch())),
    [handles]
  )
  return { states, refetch }
}
