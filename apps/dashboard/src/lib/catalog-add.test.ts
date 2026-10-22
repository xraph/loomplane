import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import type { KindInfo } from "@/client/forge/loomplane/types"
import { addCatalogService, usesOf } from "@/lib/catalog-add"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

type Call = { method: string; path: string; body: unknown }

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

// serve answers the two writes adding a service makes. Anything else is
// a request the flow shouldn't send.
function serve(edit: () => Response) {
  const calls: Call[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const path = new URL(String(input), "http://localhost").pathname
      calls.push({
        method,
        path,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      if (method === "POST" && path === "/api/v1/projects/p/manifest/edits")
        return edit()
      if (method === "POST" && path === "/api/v1/projects/p/plans")
        return json({ id: "plan_1", status: "planning" })
      throw new Error(`Unexpected request ${method} ${path}`)
    }
  )
  return calls
}

const input = {
  projectId: "p",
  environment: "staging",
  name: "gateway",
  kind: "sample",
  provider: "server" as const,
  target: "conn_1",
  version: "1",
  uses: { postgres: { resource: "db" } },
}

test("adds the resource with uses, then plans without catalog mode", async () => {
  const calls = serve(() => json({ id: "rev_2", revision: 2 }))
  const progress = vi.fn()
  await expect(addCatalogService(input, progress)).resolves.toEqual({
    planId: "plan_1",
  })
  // The generated client sends the Go field names: add_resource.
  expect(calls).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/manifest/edits",
      body: {
        add_resource: {
          name: "gateway",
          kind: "sample",
          provider: "server",
          target: "conn_1",
          version: "1",
          uses: { postgres: { resource: "db" } },
        },
      },
    },
    {
      method: "POST",
      path: "/api/v1/projects/p/plans",
      body: { environment: "staging" },
    },
  ])
  // Planning only: nothing is approved here.
  expect(calls.some((c) => c.path.endsWith("/approve"))).toBe(false)
  expect(progress).toHaveBeenCalled()
})

test("refuses a name the manifest already uses", async () => {
  const calls = serve(() => json({ message: "gateway is taken" }, 422))
  await expect(addCatalogService(input, vi.fn())).rejects.toThrow(
    "gateway is taken"
  )
  expect(calls.map((c) => c.path)).toEqual([
    "/api/v1/projects/p/manifest/edits",
  ])
})

test("leaves out the version and uses it wasn't given", async () => {
  const calls = serve(() => json({ id: "rev_2", revision: 2 }))
  await addCatalogService(
    {
      projectId: "p",
      environment: "production",
      name: "gateway",
      kind: "sample",
      provider: "kubernetes",
      target: "conn_k",
    },
    vi.fn()
  )
  expect(calls[0].body).toEqual({
    add_resource: {
      name: "gateway",
      kind: "sample",
      provider: "kubernetes",
      target: "conn_k",
    },
  })
})

test("uses says only what differs from the kind's defaults", () => {
  const dep = (name: string, optional: boolean, on: boolean) => ({
    name,
    title: name,
    kinds: ["postgres"],
    optional,
    default: on,
    external: true,
  })
  const kind: KindInfo = {
    name: "sample",
    title: "Sample",
    placement: "app-project",
    targets: ["server"],
    versions: ["1"],
    engine: false,
    dependencies: [
      dep("required", false, false),
      dep("usually-off", true, false),
      dep("usually-on", true, true),
      dep("connected", false, false),
      dep("outside", true, false),
    ],
  }
  expect(
    usesOf(kind, {
      required: { mode: "new" },
      "usually-off": { mode: "new" },
      "usually-on": { mode: "off" },
      connected: { mode: "resource", resource: "db" },
      outside: { mode: "external", external: "ep_1" },
    })
  ).toEqual({
    "usually-off": { enabled: true },
    "usually-on": { enabled: false },
    connected: { resource: "db" },
    outside: { external: "ep_1" },
  })
  expect(
    usesOf(kind, {
      required: { mode: "new" },
      "usually-off": { mode: "off" },
      "usually-on": { mode: "new" },
      connected: { mode: "new" },
      outside: { mode: "off" },
    })
  ).toBeUndefined()
})
