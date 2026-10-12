import { createLocalStorage, SESSION_STORAGE_KEY } from "@authsome/ui-core"

/**
 * Where the authsome SDK keeps the session. Local storage, so a reload
 * keeps you signed in: the SDK restores only from its own storage. The cost
 * is that a script injected into the page could read the token, which the
 * SDK's own docs call out; the dashboard ships no third-party scripts.
 */
export const authStorage = createLocalStorage()

/** sessionToken is the current session token, read fresh on every call. */
export function sessionToken(): string | null {
  const raw = window.localStorage.getItem(SESSION_STORAGE_KEY)
  if (!raw) return null
  try {
    const session = JSON.parse(raw) as { session_token?: string }
    return session.session_token ?? null
  } catch {
    return null
  }
}
