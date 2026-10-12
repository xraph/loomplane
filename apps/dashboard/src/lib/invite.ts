// An invitation waiting to be accepted. Someone following an invitation link
// often has to create an account or sign in first, which navigates away from
// the link, so its token waits in session storage (this tab only) until
// they're signed in.
import { useSyncExternalStore } from "react"

const key = "loomplane.invite"
export const acceptPath = "/invitations/accept"

// Session storage doesn't announce its own changes in the tab that made
// them, so whoever renders the accept page has to hear it here. Without
// this, accepting while already at "/" changed nothing on screen: navigate
// skips the current path and the app never re-read storage.
const listeners = new Set<() => void>()

function subscribe(cb: () => void) {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

function notify() {
  for (const cb of listeners) cb()
}

/** captureInvite keeps the token from an invitation link, if this is one. */
export function captureInvite(location: Location = window.location): void {
  if (location.pathname !== acceptPath) return
  const token = new URLSearchParams(location.search).get("token")
  if (!token) return
  window.sessionStorage.setItem(key, token)
  notify()
}

export function pendingInvite(): string | null {
  return window.sessionStorage.getItem(key)
}

export function clearInvite(): void {
  window.sessionStorage.removeItem(key)
  notify()
}

/** usePendingInvite is pendingInvite, re-rendering when it changes. */
export function usePendingInvite(): string | null {
  return useSyncExternalStore(subscribe, pendingInvite, () => null)
}
