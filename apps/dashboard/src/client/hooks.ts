import { useEffect } from "react"
import { useLiveState } from "@/lib/realtime-state"

// Client components use the generated bindings through these.
export { useInvalidate, useMutation, useQuery } from "@forge-go/client-react"

/**
 * useRefetchWhile refetches a query every interval ms while active is true,
 * such as while a project's environments are being set up. The generated
 * client has no polling of its own; changes otherwise arrive through the
 * normalized cache when a mutation touches an entity.
 */
export function useRefetchWhile(
  active: boolean,
  refetch: () => Promise<unknown>,
  interval = 1000
): void {
  const live = useLiveState().status === "live"
  useEffect(() => {
    if (!active || live) return
    const id = window.setInterval(
      () => {
        // A failed refetch is shown by the query's own error state.
        refetch().catch(() => undefined)
      },
      Math.max(interval, 15000)
    )
    return () => window.clearInterval(id)
  }, [active, refetch, interval, live])
}
