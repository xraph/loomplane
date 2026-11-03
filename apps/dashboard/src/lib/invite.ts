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
  // Session storage holds it now. Left in the URL, the token sits in
  // history, gets saved as the address to come back to after sign-in, and
  // shows in screenshots. The page keeps its own address without it.
  if (location === window.location) {
    window.history.replaceState(window.history.state, "", acceptPath)
  }
  notify()
}

export function pendingInvite(): string | null {
  return window.sessionStorage.getItem(key)
}

// Set once this tab has accepted or put off an invitation, so the accept
// page can tell "you're done here" from a link that came without a token.
let settled = false

export function clearInvite(): void {
  settled = true
  window.sessionStorage.removeItem(key)
  notify()
}

/** inviteSettled says whether this tab has already dealt with an invitation. */
export function inviteSettled(): boolean {
  return settled
}

/** usePendingInvite is pendingInvite, re-rendering when it changes. */
export function usePendingInvite(): string | null {
  return useSyncExternalStore(subscribe, pendingInvite, () => null)
}
