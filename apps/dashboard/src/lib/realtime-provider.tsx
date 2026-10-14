import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
} from "react"
import { forgeClient } from "@/client/runtime"
import { useOrganization } from "@/lib/organization"
import { LoomplaneRealtime } from "@/lib/realtime"
import { publishLiveState } from "@/lib/realtime-state"

const Context = createContext<LoomplaneRealtime | null>(null)
export function RealtimeProvider({ children }: { children: ReactNode }) {
  const { session } = useOrganization()
  const realtime = useMemo(
    () => new LoomplaneRealtime({ cache: forgeClient, orgId: session.orgId }),
    [session.orgId]
  )
  useEffect(() => {
    const unsubscribe = realtime.subscribe(() =>
      publishLiveState(realtime.state())
    )
    realtime.start()
    const visible = () => {
      if (document.visibilityState === "visible") void realtime.synchronize()
    }
    document.addEventListener("visibilitychange", visible)
    return () => {
      document.removeEventListener("visibilitychange", visible)
      realtime.stop()
      unsubscribe()
    }
  }, [realtime])
  return <Context.Provider value={realtime}>{children}</Context.Provider>
}
export function useRealtime() {
  return useContext(Context)
}
