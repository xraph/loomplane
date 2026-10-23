import { afterEach, expect, it, vi } from "vitest"

import { RESTClient } from "@/client/forge/loomplane/rest"

afterEach(() => vi.unstubAllGlobals())

it("omits native copy pagination when you request the first page", async () => {
  const fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ copies: [] }), {
        headers: { "Content-Type": "application/json" },
      })
  )
  vi.stubGlobal("fetch", fetch)
  const client = new RESTClient({ baseURL: "https://loomplane.example" })

  await client.nativeRecoveryCopies.list("project")

  const [url] = fetch.mock.calls[0] as unknown as [string]
  expect(new URL(url).pathname).toBe(
    "/api/v1/projects/project/native-recovery-copies"
  )
  expect(new URL(url).search).toBe("")
})

it("keeps explicit native copy scope and cursor values on the wire", async () => {
  const fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ copies: [], next_before: "next" }), {
        headers: { "Content-Type": "application/json" },
      })
  )
  vi.stubGlobal("fetch", fetch)
  const client = new RESTClient({ baseURL: "https://loomplane.example" })

  const page = await client.nativeRecoveryCopies.list(
    "project",
    "env&one",
    "copy/one",
    1,
    "orders&reports"
  )

  const [url] = fetch.mock.calls[0] as unknown as [string]
  const query = new URL(url).searchParams
  expect(query.get("environment_id")).toBe("env&one")
  expect(query.get("before")).toBe("copy/one")
  expect(query.get("limit")).toBe("1")
  expect(query.get("resource")).toBe("orders&reports")
  expect(page.nextBefore).toBe("next")
})
