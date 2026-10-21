import { useEffect } from "react"

// Recovery has no realtime event contract. A healthy live connection cannot
// replace these reads of persisted job state.
export function useRecoveryPolling(
  active: boolean,
  refresh: () => Promise<unknown>,
  idleInterval: number | null = 15000
) {
  useEffect(() => {
    if (!active && idleInterval === null) return
    const timer = window.setInterval(
      () => {
        void refresh().catch(() => undefined)
      },
      active ? 2000 : (idleInterval ?? 15000)
    )
    return () => window.clearInterval(timer)
  }, [active, refresh, idleInterval])
}
