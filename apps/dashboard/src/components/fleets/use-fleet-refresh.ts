import { useEffect } from "react"

// Fleet reads refresh independently until their own event stream is connected.
export function useFleetRefresh(
  active: boolean,
  refresh: () => Promise<unknown>
) {
  useEffect(() => {
    if (!active) return
    let running = false
    const timer = window.setInterval(() => {
      if (running || document.visibilityState === "hidden") return
      running = true
      void refresh()
        .catch(() => undefined)
        .finally(() => {
          running = false
        })
    }, 15000)
    return () => window.clearInterval(timer)
  }, [active, refresh])
}
