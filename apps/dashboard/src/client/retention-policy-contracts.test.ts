import { afterEach, expect, it, vi } from "vitest"

import {
  useRetentionPoliciesGet,
  useRetentionPoliciesHistory,
  useRetentionPoliciesSet,
} from "@/client/retention-policies"
import { forgeClient } from "@/client/runtime"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

it("refreshes mounted unset state and empty history across backup aliases after first enable", async () => {
  let enabled = false
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const path = new URL(url, "http://localhost")
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
          events: enabled
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
          configured: enabled,
          ...(enabled
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
  const state = useRetentionPoliciesGet({ path: { id: "backup-a" } })
  const alias = useRetentionPoliciesGet({ path: { id: "backup-b" } })
  const history = useRetentionPoliciesHistory({ path: { id: "backup-b" } })
  const release = [
    state.subscribe(() => {}),
    alias.subscribe(() => {}),
    history.subscribe(() => {}),
  ]
  try {
    expect((await state.fetch()).configured).toBe(false)
    expect((await alias.fetch()).configured).toBe(false)
    expect((await history.fetch()).events).toEqual([])
    await useRetentionPoliciesSet({
      path: { id: "backup-a" },
      body: {
        revision: 0,
        enabled: true,
        policy: { olderThanDays: 30, keepLast: 7 },
      },
    })
    await vi.waitFor(() => {
      expect(state.getState().data?.configured).toBe(true)
      expect(alias.getState().data?.configured).toBe(true)
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
  const first = useRetentionPoliciesHistory({
    path: { id: "backup-a" },
    query: { limit: 1 },
  })
  const second = useRetentionPoliciesHistory({
    path: { id: "backup-a" },
    query: { limit: 1, before: "2" },
  })
  await first.fetch()
  await second.fetch()
  expect(first.getState().data?.events[0].revision).toBe(2)
  expect(first.getState().data?.nextBefore).toBe("2")
  expect(second.getState().data?.events[0].revision).toBe(1)
  expect(second.getState().data?.nextBefore).toBeUndefined()
})
