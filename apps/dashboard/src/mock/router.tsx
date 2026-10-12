// The mock routes on the URL hash, so it works from a single static entry
// (mock/index.html) and keeps out of the real dashboard's routes. A hash
// can carry a query too ("#/deployments?status=live"), which the sidebar's
// deep links use.
import { useSyncExternalStore } from "react"

function subscribe(cb: () => void) {
  window.addEventListener("hashchange", cb)
  return () => window.removeEventListener("hashchange", cb)
}

function hash() {
  return window.location.hash.slice(1) || "/"
}

/** useRoute returns the hash path without its query. */
export function useRoute(): string {
  const h = useSyncExternalStore(subscribe, hash, () => "/")
  return h.split("?")[0] || "/"
}

/** useQuery returns the hash's query parameters. */
export function useQuery(): URLSearchParams {
  const h = useSyncExternalStore(subscribe, hash, () => "/")
  return new URLSearchParams(h.split("?")[1] ?? "")
}

/** to turns an app path into the href that reaches it. */
export function to(path: string): string {
  return `#${path}`
}

export function navigate(path: string) {
  window.location.hash = path
}

/** match returns the route's parameters when path fits pattern ("/projects/:id"). */
export function match(
  pattern: string,
  path: string
): Record<string, string> | null {
  const want = pattern.split("/").filter(Boolean)
  const got = path.split("/").filter(Boolean)
  if (want.length !== got.length) return null
  const params: Record<string, string> = {}
  for (let i = 0; i < want.length; i++) {
    const w = want[i] as string
    const g = got[i] as string
    if (w.startsWith(":")) params[w.slice(1)] = decodeURIComponent(g)
    else if (w !== g) return null
  }
  return params
}
