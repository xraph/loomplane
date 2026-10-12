import { useAuth } from "@authsome/ui-react"
import { useEffect } from "react"

import { setSessionRefresher } from "./runtime"

/**
 * Lets the API client renew the session on a 401 through the authsome SDK,
 * which owns the refresh token and serializes rotation across tabs. The
 * client never calls /v1/refresh itself, so two rotations can't race.
 */
export function SessionRefreshBridge() {
  const { manager } = useAuth()
  useEffect(() => {
    setSessionRefresher(() => manager.refreshNow())
    return () => setSessionRefresher(null)
  }, [manager])
  return null
}
