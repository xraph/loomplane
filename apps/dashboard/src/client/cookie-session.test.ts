// The cookie mode patches/@authsome__ui-core@1.8.3.patch adds to authsome's
// AuthManager: the session stays in the backend's httpOnly cookie, so no
// token is stored, sent or kept, and a reload asks the backend who's signed
// in. These drive the real manager over a fake fetch that records every
// request.
import { AuthManager, type TokenStorage } from "@authsome/ui-core"
import { describe, expect, it, vi } from "vitest"

const base = "http://loomplane.test/authsome"
const user = { id: "ausr_1", email: "ada@example.com" }

type Sent = { url: string; init: RequestInit }

/** backend answers each path with a status and body, recording the calls. */
function backend(answers: Record<string, () => Response>) {
  const sent: Sent[] = []
  const fetch = vi.fn(
    async (url: string | URL | Request, init?: RequestInit) => {
      const u = String(url)
      sent.push({ url: u, init: init ?? {} })
      const path = new URL(u).pathname.replace("/authsome", "")
      const answer = answers[path]
      if (!answer) throw new TypeError(`no answer for ${path}`)
      return answer()
    }
  )
  return { fetch: fetch as unknown as typeof globalThis.fetch, sent }
}

const json = (status: number, body: unknown) => () =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

/** storage that fails the test if a session is written to it. */
function watchedStorage(): TokenStorage & { writes: string[] } {
  const writes: string[] = []
  return {
    writes,
    getItem: () => null,
    setItem: (key: string) => {
      writes.push(key)
    },
    removeItem: () => {},
  }
}

function authorization(s: Sent): string | null {
  return new Headers(s.init.headers).get("Authorization")
}

describe("authsome's cookie session", () => {
  it("restores a session by asking the backend, sending only the cookie", async () => {
    const { fetch, sent } = backend({ "/v1/me": json(200, user) })
    const auth = new AuthManager({ baseURL: base, fetch, session: "cookie" })
    await auth.initialize()

    expect(auth.getState().status).toBe("authenticated")
    expect(auth.getUser()).toMatchObject({ email: "ada@example.com" })
    expect(auth.getSessionToken()).toBeFalsy()
    expect(sent).toHaveLength(1)
    expect(sent[0].init.credentials).toBe("include")
    expect(authorization(sent[0])).toBeNull()
  })

  it("is signed out when the backend refuses the cookie", async () => {
    const { fetch } = backend({
      "/v1/me": json(401, { error: "unauthorized" }),
    })
    const auth = new AuthManager({ baseURL: base, fetch, session: "cookie" })
    await auth.initialize()
    expect(auth.getState().status).toBe("unauthenticated")
  })

  it("doesn't sign you out when the backend can't be reached", async () => {
    const { fetch } = backend({})
    const auth = new AuthManager({ baseURL: base, fetch, session: "cookie" })
    await auth.initialize()
    expect(auth.getState().status).toBe("unknown")
    auth.destroy()
  })

  it("keeps no token from signing in, and stores nothing", async () => {
    const storage = watchedStorage()
    const { fetch, sent } = backend({
      "/v1/signin": json(200, {
        user,
        session_token: "secret-session",
        refresh_token: "secret-refresh",
        expires_at: new Date(Date.now() + 3_600_000).toISOString(),
      }),
    })
    const auth = new AuthManager({
      baseURL: base,
      fetch,
      storage,
      session: "cookie",
    })
    await auth.signIn({ email: "ada@example.com", password: "x" })

    expect(auth.getState().status).toBe("authenticated")
    expect(auth.getSessionToken()).toBeFalsy()
    expect(JSON.stringify(auth.getState())).not.toContain("secret")
    expect(storage.writes).toEqual([])
    expect(sent[0].init.credentials).toBe("include")
    auth.destroy()
  })

  it("signs out through the backend, which clears the cookie", async () => {
    const { fetch, sent } = backend({
      "/v1/me": json(200, user),
      "/v1/signout": json(200, { status: "ok" }),
    })
    const auth = new AuthManager({ baseURL: base, fetch, session: "cookie" })
    await auth.initialize()
    await auth.signOut()

    expect(auth.getState().status).toBe("unauthenticated")
    const out = sent.find((s) => s.url.endsWith("/v1/signout"))
    expect(out?.init.method).toBe("POST")
    expect(out?.init.credentials).toBe("include")
    expect(out && authorization(out)).toBeNull()
  })

  it("leaves the token mode as it was", async () => {
    const { fetch, sent } = backend({ "/v1/me": json(200, user) })
    const auth = new AuthManager({ baseURL: base, fetch })
    await auth.initialize()
    // Nothing stored means signed out, without asking the backend.
    expect(auth.getState().status).toBe("unauthenticated")
    expect(sent).toHaveLength(0)
  })
})
