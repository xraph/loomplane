import { useAuth } from "@authsome/ui-react"
import { useEffect } from "react"

import { setSessionRefresher } from "./runtime"

/**
 * Lets the API client check the session on a 401 through the authsome SDK,
 * which asks the backend whether the cookie still names a session. When it
 * doesn't, the SDK signs out, so the dashboard shows sign-in.
 */
export function SessionRefreshBridge() {
  const { manager } = useAuth()
  useEffect(() => {
    setSessionRefresher(async () => {
      await manager.refreshNow()
      return manager.getState().status === "authenticated"
    })
    return () => setSessionRefresher(null)
  }, [manager])
  return null
}
