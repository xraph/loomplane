// The cloud someone went to sign in with. The cloud sends them back to one
// callback page, which can't tell which cloud it was, so the name waits in
// session storage (this tab only). It's only a label for that page.
const key = "loomplane.sign-in-cloud"

/** rememberSignInCloud keeps the title of the cloud a sign-in goes to. */
export function rememberSignInCloud(title: string): void {
  try {
    window.sessionStorage.setItem(key, title)
  } catch {
    // Without storage the callback says "the cloud" instead.
  }
}

/** signInCloud is the title rememberSignInCloud kept, or "". */
export function signInCloud(): string {
  try {
    return window.sessionStorage.getItem(key) ?? ""
  } catch {
    return ""
  }
}

// Vercel may send someone back without the state it was given, so the state
// a Vercel sign-in started with waits here (this browser) for ten minutes.
const vercelKey = "loomplane.sign-in.vercel"
const vercelTTL = 10 * 60 * 1000

/** rememberVercelState keeps the state a Vercel sign-in started with. */
export function rememberVercelState(state: string): void {
  try {
    window.localStorage.setItem(
      vercelKey,
      JSON.stringify({ state, at: Date.now() })
    )
  } catch {
    // Without storage a Vercel callback that drops the state can't finish.
  }
}

/**
 * takeVercelState is the state rememberVercelState kept, or "" when there is
 * none or it is older than ten minutes. The entry is removed either way.
 */
export function takeVercelState(): string {
  try {
    const raw = window.localStorage.getItem(vercelKey)
    if (raw === null) return ""
    window.localStorage.removeItem(vercelKey)
    const kept = JSON.parse(raw) as { state?: unknown; at?: unknown }
    if (typeof kept.state !== "string" || typeof kept.at !== "number") return ""
    const age = Date.now() - kept.at
    return age >= 0 && age < vercelTTL ? kept.state : ""
  } catch {
    return ""
  }
}
