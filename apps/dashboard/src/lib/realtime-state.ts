import { useSyncExternalStore } from "react"

export type LiveState = {
  status: "live" | "reconnecting" | "stale" | "denied"
  transport: "ws" | "sse"
  lastSync: number
  gap: boolean
}
const initial: LiveState = {
  status: "stale",
  transport: "ws",
  lastSync: 0,
  gap: false,
}
let current = initial
const listeners = new Set<() => void>()
export function publishLiveState(next: LiveState) {
  current = next
  listeners.forEach((fn) => fn())
}
export function useLiveState() {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    () => current,
    () => initial
  )
}
