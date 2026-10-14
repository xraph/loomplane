import { ClientProvider } from "@forge-go/client-react"
import { act, renderHook, waitFor } from "@testing-library/react"
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

describe("older runs", () => {
  const project = {
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
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  }
  const plan = (n: number) => ({
    id: `plan_${n}`,
    org_id: "org_1",
    project_id: "proj_1",
    environment: "production",
    environment_id: "env_1",
    status: "rejected",
    actions: [],
    connections: {},
    created_at: `2026-10-${String(n).padStart(2, "0")}T00:00:00Z`,
    created_by: "user_1",
    decided_by: "user_1",
    decision_note: "",
    error: "",
    executable: false,
    manifest_id: "man_1",
    manifest_revision: 1,
    problems: [],
    warnings: [],
    revision: "r1",
    sources: [],
  })

  // A fake API that pages plans two at a time, newest first, with the
  // last row's ID as its cursor. A limit of 100 reads everything older.
  function serve(plans: ReturnType<typeof plan>[]) {
    vi.stubGlobal("fetch", async (url: string) => {
      const u = new URL(url, "http://localhost")
      if (u.pathname === "/api/v1/projects")
        return json({ projects: [project] })
      if (u.pathname.endsWith("/deployments")) return json({ deployments: [] })
      if (u.pathname.endsWith("/plans")) {
        const before = u.searchParams.get("before")
        const from = before ? plans.findIndex((p) => p.id === before) + 1 : 0
        const size = u.searchParams.get("limit") === "100" ? 100 : 2
        const page = plans.slice(from, from + size)
        const more = from + size < plans.length
        return json({ plans: page, next: more ? page.at(-1)?.id : undefined })
      }
      throw new Error(`Unexpected request: ${u.pathname}`)
    })
  }
  const ids = (r: { current: { runs: { id: string }[] } }) =>
    r.current.runs.map((run) => run.id)

  it("reads older pages on more() and stops at the oldest run", async () => {
    serve([5, 4, 3, 2, 1].map(plan))
    const { result } = renderHook(() => useRuns(), { wrapper })
    await waitFor(() => expect(ids(result)).toEqual(["plan_5", "plan_4"]))
    expect(result.current.whole).toBe("2026-10-04T00:00:00Z")

    act(() => result.current.more())
    await waitFor(() => expect(ids(result)).toHaveLength(4))
    act(() => result.current.more())
    await waitFor(() =>
      expect(ids(result)).toEqual([
        "plan_5",
        "plan_4",
        "plan_3",
        "plan_2",
        "plan_1",
      ])
    )
    expect(result.current.whole).toBeUndefined()
  })

  it("keeps the run a new one pushed off the first page", async () => {
    const plans = [5, 4, 3, 2, 1].map(plan)
    serve(plans)
    const { result } = renderHook(() => useRuns(), { wrapper })
    await waitFor(() => expect(ids(result)).toHaveLength(2))
    act(() => result.current.more())
    await waitFor(() => expect(ids(result)).toHaveLength(4))

    // A new run: the first page is now 6 and 5, and 4 is in neither it nor
    // the older page read from where the first page used to end.
    plans.unshift(plan(6))
    await act(() => result.current.refetch())
    await waitFor(() =>
      expect(ids(result)).toEqual([
        "plan_6",
        "plan_5",
        "plan_4",
        "plan_3",
        "plan_2",
        "plan_1",
      ])
    )
  })
})
