import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { deployDatabase } from "@/lib/catalog"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

it("prepares a standalone component plan for review without approving it", async () => {
  const calls: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      calls.push(path)
      let response: unknown
      if (path.endsWith("/projects") || path.endsWith("/projects/project"))
        response = { id: "project", status: "ready" }
      else if (path.endsWith("/manifest")) response = { id: "manifest" }
      else if (path.endsWith("/plans") || path.endsWith("/plans/plan"))
        response = { id: "plan", status: "ready" }
      else throw new Error(`Unexpected ${init?.method} ${path}`)
      return new Response(JSON.stringify(response), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  const reviewed = vi.fn()
  const project = await deployDatabase(
    { name: "Search", target: "server", daily: false, kind: "opensearch" },
    vi.fn(),
    vi.fn(),
    reviewed
  )
  expect(project).toBe("project")
  expect(reviewed).toHaveBeenCalledWith("plan")
  expect(calls.some((path) => path.endsWith("/approve"))).toBe(false)
})
