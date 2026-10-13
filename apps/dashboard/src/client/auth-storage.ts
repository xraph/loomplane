import { createLocalStorage, SESSION_STORAGE_KEY } from "@authsome/ui-core"

/**
 * What the authsome SDK keeps in the browser: its cached client config, in
 * local storage so a reload needn't wait for it. The session isn't here. It
 * lives in the httpOnly cookie Loomplane sets on sign-in, which no script
 * can read (session="cookie" on the AuthProvider).
 */
export const authStorage = createLocalStorage()

/**
 * forgetStoredSession removes the session tokens the dashboard kept in local
 * storage before it moved to the cookie. Nothing reads them now, and any
 * script on the page could.
 */
export function forgetStoredSession(): void {
  try {
    window.localStorage.removeItem(SESSION_STORAGE_KEY)
  } catch {
    // Storage that can't be reached holds nothing to forget.
  }
}
