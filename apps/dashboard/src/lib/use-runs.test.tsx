import { ClientProvider } from "@forge-go/client-react"
import { renderHook, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { useRunning, useRuns } from "@/lib/use-runs"

function wrapper({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

describe("organization-wide runs", () => {
  it("settles a failed projects request so the page can show retry instead of loading forever", async () => {
    vi.stubGlobal("fetch", async () =>
      json({ error: "Projects unavailable" }, 503)
    )
    const { result } = renderHook(() => useRuns(), { wrapper })
    await waitFor(() => expect(result.current.error).toBeDefined())
    expect(result.current.loaded).toBe(true)
    expect(result.current.runs).toEqual([])
  })

  it("reports failed binding reads instead of claiming nothing is running", async () => {
    vi.stubGlobal("fetch", async (url: string) => {
      const path = new URL(url, "http://localhost").pathname
      if (path === "/api/v1/projects")
        return json({
          projects: [
            {
              id: "proj_1",
              org_id: "org_1",
              name: "Shop",
              slug: "shop",
              description: "",
              icon: "box",
              color: "blue",
              repositories: [],
              status: "ready",
              created_by: "user_1",
              created_at: "2026-10-13T00:00:00Z",
              updated_at: "2026-10-13T00:00:00Z",
            },
          ],
        })
      if (path.endsWith("/environments"))
        return json({
          environments: [
            {
              id: "env_1",
              org_id: "org_1",
              updated_at: "2026-10-13T00:00:00Z",
              project_id: "proj_1",
              name: "production",
              kind: "production",
              created_by: "user_1",
              created_at: "2026-10-13T00:00:00Z",
            },
          ],
        })
      if (path.endsWith("/bindings"))
        return json({ error: "Bindings unavailable" }, 503)
      throw new Error(`Unexpected request: ${path}`)
    })
    const { result } = renderHook(() => useRunning(), { wrapper })
    await waitFor(() => expect(result.current.error).toBeDefined(), {
      timeout: 5000,
    })
    expect(result.current.loaded).toBe(true)
  })
})
