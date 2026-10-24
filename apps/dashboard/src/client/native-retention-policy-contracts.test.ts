import { afterEach, expect, it, vi } from "vitest"

import {
  useNativeRetentionPoliciesGet,
  useNativeRetentionPoliciesHistory,
  useNativeRetentionPoliciesSet,
} from "@/client/native-retention-policies"
import { forgeClient } from "@/client/runtime"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

it("refreshes unset policy and history without changing another source", async () => {
  let enabled = false
  let otherReads = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const path = new URL(url, "http://localhost")
      const selected = path.pathname.includes("/source-a/")
      if (!selected && init?.method !== "PUT") otherReads++
      const visible = enabled && selected
      let body: unknown
      if (init?.method === "PUT") {
        enabled = true
        body = {
          id: "policy",
          revision: 1,
          enabled: true,
          policy: { older_than_days: 30, keep_last: 7 },
        }
      } else if (path.pathname.endsWith("/history")) {
        body = {
          events: visible
            ? [
                {
                  revision: 1,
                  enabled: true,
                  authorized_by: "owner",
                  policy: { older_than_days: 30, keep_last: 7 },
                },
              ]
            : [],
        }
      } else {
        body = {
          configured: visible,
          ...(visible
            ? { policy: { id: "policy", revision: 1, enabled: true } }
            : {}),
        }
      }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    })
  )
  const state = useNativeRetentionPoliciesGet({ path: { id: "source-a" } })
  const alias = useNativeRetentionPoliciesGet({ path: { id: "source-b" } })
  const history = useNativeRetentionPoliciesHistory({
    path: { id: "source-a" },
  })
  const release = [
    state.subscribe(() => {}),
    alias.subscribe(() => {}),
    history.subscribe(() => {}),
  ]
  try {
    expect((await state.fetch()).configured).toBe(false)
    expect((await alias.fetch()).configured).toBe(false)
    expect((await history.fetch()).events).toEqual([])
    await useNativeRetentionPoliciesSet({
      path: { id: "source-a" },
      body: {
        revision: 0,
        enabled: true,
        policy: { olderThanDays: 30, keepLast: 7 },
      },
    })
    await vi.waitFor(() => {
      expect(state.getState().data?.configured).toBe(true)
      expect(alias.getState().data?.configured).toBe(false)
      expect(otherReads).toBeGreaterThanOrEqual(2)
      expect(history.getState().data?.events).toHaveLength(1)
    })
  } finally {
    release.forEach((unsubscribe) => unsubscribe())
  }
})

it("keeps independent policy history cursors", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const path = new URL(url, "http://localhost")
      const before = path.searchParams.get("before") ?? ""
      return new Response(
        JSON.stringify({
          events: [
            {
              revision: before ? 1 : 2,
              enabled: !before,
              authorized_by: "owner",
              policy: { older_than_days: 30, keep_last: 7 },
            },
          ],
          ...(before ? {} : { next_before: "2" }),
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      )
    })
  )
  const first = useNativeRetentionPoliciesHistory({
    path: { id: "source-a" },
    query: { limit: 1 },
  })
  const second = useNativeRetentionPoliciesHistory({
    path: { id: "source-a" },
    query: { limit: 1, before: "2" },
  })
  await first.fetch()
  await second.fetch()
  expect(first.getState().data?.events[0].revision).toBe(2)
  expect(first.getState().data?.nextBefore).toBe("2")
  expect(second.getState().data?.events[0].revision).toBe(1)
  expect(second.getState().data?.nextBefore).toBeUndefined()
})
