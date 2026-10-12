import { SESSION_STORAGE_KEY } from "@authsome/ui-core"
import { afterEach, describe, expect, it, vi } from "vitest"

import { useListOrganizations } from "@/client/forge/authsome/hooks"
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import {
  forgeClient,
  ORG_HEADER,
  serviceFor,
  setAuthsomeApp,
  setTabOrganization,
} from "@/client/runtime"

function signIn(token: string) {
  window.localStorage.setItem(
    SESSION_STORAGE_KEY,
    JSON.stringify({
      session_token: token,
      refresh_token: "r",
      expires_at: "2099-01-01T00:00:00Z",
    })
  )
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  setTabOrganization("")
  setAuthsomeApp({ publishableKey: "", appId: "" })
  forgeClient.clear()
})

describe("serviceFor", () => {
  it("routes each prefix to its generated client", () => {
    expect(serviceFor("/api/v1/projects").orgScoped).toBe(true)
    expect(serviceFor("/authsome/v1/orgs").orgScoped).toBe(false)
    expect(() => serviceFor("/elsewhere")).toThrow(/no generated client/)
  })
})

describe("requests", () => {
  it("send the session and the tab's organization to Loomplane", async () => {
    const fetch = vi.fn(async () => json({ projects: [] }))
    vi.stubGlobal("fetch", fetch)
    signIn("tok_1")
    setTabOrganization("aorg_1")

    const list = await useProjectsList().fetch()

    expect(list.projects).toEqual([])
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toContain("/api/v1/projects")
    const headers = new Headers(init.headers)
    expect(headers.get("Authorization")).toBe("Bearer tok_1")
    expect(headers.get(ORG_HEADER)).toBe("aorg_1")
    expect(headers.get("X-Publishable-Key")).toBeNull()
  })

  it("send the publishable key, not the org, to authsome", async () => {
    const fetch = vi.fn(async () => json({ organizations: [] }))
    vi.stubGlobal("fetch", fetch)
    signIn("tok_2")
    setTabOrganization("aorg_1")
    setAuthsomeApp({ publishableKey: "pk_test", appId: "aapp_1" })

    const orgs = await useListOrganizations().fetch()

    expect(orgs.organizations).toEqual([])
    const [, init] = fetch.mock.calls[0] as unknown as [string, RequestInit]
    const headers = new Headers(init.headers)
    expect(headers.get("Authorization")).toBe("Bearer tok_2")
    expect(headers.get("X-Publishable-Key")).toBe("pk_test")
    expect(headers.get(ORG_HEADER)).toBeNull()
  })

  it("decode snake_case responses to camelCase", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        json({
          projects: [
            {
              id: "proj_1",
              org_id: "aorg_1",
              name: "Engine",
              slug: "engine",
              description: "",
              repositories: ["a/b"],
              status: "ready",
              created_by: "ausr_1",
              created_at: "2026-10-12T03:10:26Z",
              updated_at: "2026-10-12T03:10:26Z",
            },
          ],
        })
      )
    )
    const list = await useProjectsList().fetch()
    expect(list.projects[0]?.repositories).toEqual(["a/b"])
    expect(list.projects[0]?.orgId).toBe("aorg_1")
  })
})
