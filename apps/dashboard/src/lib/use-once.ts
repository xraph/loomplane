import { useCallback, useRef } from "react"

/**
 * useOnce runs an action at most once at a time per key. A second click
 * that lands before React draws the first one's pending state would
 * otherwise send a second request, which for a removal or a cancel is a
 * second change. Calls made while one is running are dropped.
 */
export function useOnce(): (
  key: string,
  action: () => Promise<void>
) => Promise<void> {
  const running = useRef(new Set<string>())
  return useCallback(async (key, action) => {
    if (running.current.has(key)) return
    running.current.add(key)
    try {
      await action()
    } finally {
      running.current.delete(key)
    }
  }, [])
}
