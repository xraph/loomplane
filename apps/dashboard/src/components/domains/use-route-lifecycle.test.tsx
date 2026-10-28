import { ClientProvider, useQuery } from "@forge-go/client-react"
import { act, renderHook, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import {
  useDomainRoutesList,
  useDomainsOverview,
  useDomainsProject,
} from "@/client/forge/loomplane/hooks"
import { forgeClient } from "@/client/runtime"
import { type Route, useRouteLifecycle } from "./use-route-lifecycle"

function wrapper({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const zero = "0001-01-01T00:00:00Z"

function operation(id: string, status: string) {
  return {
    id,
    kind: "apply",
    status,
    review: "review-token",
    attempt: 1,
    revision: 1,
    generation: 0,
    message: "",
    created_at: zero,
    expires_at: zero,
    not_before: zero,
    dns_attempted: false,
    desired_record: {},
    route: { id: "route_w", revision: 1 },
  }
}

type Call = { method: string; path: string; body?: unknown }

function stub(post: (path: string) => Response | undefined = () => undefined) {
  const calls: Call[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      calls.push({
        method,
        path,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      if (method === "POST") {
        const custom = post(path)
        if (custom) return custom
        if (path.endsWith("/domain-routes/preview"))
          return Response.json(operation("routeop_1", "preview"))
        return Response.json(operation(path.split("/")[5], "pending"))
      }
      if (path === "/api/v1/projects/proj/domains")
        return Response.json({ domains: [] })
      if (path === "/api/v1/projects/proj/domain-routes")
        return Response.json({ routes: [] })
      if (path === "/api/v1/domains")
        return Response.json({ zones: [], registrations: [] })
      return Response.json({}, { status: 404 })
    }
  )
  return calls
}

// The hook with the three views it refreshes held open beside it.
function mount() {
  return renderHook(
    () => {
      useQuery(useDomainsProject, { path: { projectId: "proj" } })
      useQuery(useDomainRoutesList, { path: { projectId: "proj" } })
      useQuery(useDomainsOverview)
      return useRouteLifecycle("proj")
    },
    { wrapper }
  )
}

const reads = (calls: Call[]) =>
  [
    "/api/v1/projects/proj/domains",
    "/api/v1/projects/proj/domain-routes",
    "/api/v1/domains",
  ].map(
    (path) => calls.filter((c) => c.method === "GET" && c.path === path).length
  )

it("previews and applies a route in two steps", async () => {
  // preview body { zone_id, cloud_id, environment_id, service, hostname: "*.acme.dev", port: 8080 }; apply body { review }.
  const calls = stub()
  const { result } = mount()
  await waitFor(() => expect(reads(calls)).toEqual([1, 1, 1]))

  let op = await act(() =>
    result.current.preview({
      zoneId: "zone_dev",
      cloudId: "cloud_1",
      environmentId: "prod",
      service: "web",
      hostname: "*.acme.dev",
      port: 8080,
    })
  )
  const preview = calls.find((c) => c.method === "POST")
  expect(preview).toEqual({
    method: "POST",
    path: "/api/v1/projects/proj/domain-routes/preview",
    body: {
      zone_id: "zone_dev",
      cloud_id: "cloud_1",
      environment_id: "prod",
      service: "web",
      hostname: "*.acme.dev",
      port: 8080,
    },
  })
  expect(op.id).toBe("routeop_1")
  expect(op.review).toBe("review-token")
  // A preview changes nothing, so nothing is read again.
  expect(reads(calls)).toEqual([1, 1, 1])

  op = await act(() => result.current.apply(op))
  expect(calls.filter((c) => c.method === "POST").at(-1)).toEqual({
    method: "POST",
    path: "/api/v1/projects/proj/route-operations/routeop_1/apply",
    body: { review: "review-token" },
  })
  expect(op.status).toBe("pending")
  await waitFor(() => expect(reads(calls)).toEqual([2, 2, 2]))
  expect(result.current.error).toBe("")
  expect(result.current.pending).toBe(false)
})

it("removes and retries with the route's revision and the operation", async () => {
  const calls = stub()
  const { result } = mount()
  await waitFor(() => expect(reads(calls)).toEqual([1, 1, 1]))
  await act(() =>
    result.current.remove({ id: "route_w", revision: 4 } as Route)
  )
  expect(calls.filter((c) => c.method === "POST").at(-1)).toEqual({
    method: "POST",
    path: "/api/v1/projects/proj/domain-routes/route_w/remove",
    body: { revision: 4 },
  })
  await waitFor(() => expect(reads(calls)).toEqual([2, 2, 2]))
  await act(() => result.current.retry("routeop_9"))
  expect(calls.filter((c) => c.method === "POST").at(-1)).toMatchObject({
    path: "/api/v1/projects/proj/route-operations/routeop_9/retry",
  })
  await waitFor(() => expect(reads(calls)).toEqual([3, 3, 3]))
})

it("says what the server refused", async () => {
  let message =
    "wildcard routes need a verified Cloudflare zone with a DNS challenge credential"
  stub(() => Response.json({ message }, { status: 422 }))
  const { result } = mount()
  const input = {
    zoneId: "zone_org",
    cloudId: "cloud_1",
    environmentId: "prod",
    service: "web",
    hostname: "*.acme.org",
    port: 8080,
  }
  await act(() => expect(result.current.preview(input)).rejects.toThrow())
  expect(result.current.error).toBe(
    "Wildcard routes need a verified Cloudflare zone with a DNS challenge credential."
  )
  // The bare invalid-request text says nothing a person can act on.
  message = "invalid infrastructure request"
  input.hostname = "a.*.acme.org"
  await act(() => expect(result.current.preview(input)).rejects.toThrow())
  expect(result.current.error).toBe(
    "Check the hostname and port, then preview again."
  )
  // The sentinel in front of a reason is dropped, and the reason kept as written.
  message =
    "Invalid infrastructure request: x.a.acme.dev belongs to a.acme.dev. Pick that domain for it, or ask an admin to grant it to this project"
  input.hostname = "x.a.acme.dev"
  await act(() => expect(result.current.preview(input)).rejects.toThrow())
  expect(result.current.error).toBe(
    "x.a.acme.dev belongs to a.acme.dev. Pick that domain for it, or ask an admin to grant it to this project."
  )
})
