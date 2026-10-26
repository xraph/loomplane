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
