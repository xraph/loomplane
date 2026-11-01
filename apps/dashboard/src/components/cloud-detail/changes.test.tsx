import { ClientProvider } from "@forge-go/client-react"
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
  Outlet,
} from "@tanstack/react-router"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import type { Profile } from "@/client/forge/loomplane/types"
import { publishLiveState } from "@/lib/realtime-state"
import { setWorkspaceSlug, workspaceRewrite } from "@/lib/workspace-url"
import { Changes } from "./changes"
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  forgeClient.clear()
  vi.unstubAllGlobals()
  setWorkspaceSlug("")
  publishLiveState({
    status: "stale",
    transport: "ws",
    lastSync: 0,
    gap: false,
  })
})
it("names lifecycle changes, refreshes persisted history on reconnect and follows the workspace operation link", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] })
  let reads = 0
  let kind = "node-pool-update"
  const writes: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (init?.method && init.method !== "GET") writes.push(path)
      if (path.includes("/cluster-operations")) {
        reads++
        return Response.json({
          items: [
            {
              id: "op",
              kind,
              status: "accepted",
              created_at: new Date().toISOString(),
              input: {
                spec: { name: "Long worker cluster" },
                ref: { id: "physical" },
              },
              steps: [],
            },
          ],
        })
      }
      return Response.json({ operations: [], syncs: [], users: [] })
    }
  )
  const root = createRootRoute({ component: Outlet })
  const changes = createRoute({
    getParentRoute: () => root,
    path: "/changes",
    component: () => (
      <Changes
        scope={{ cloud: { id: "cloud" } as Profile, project: "" }}
        targets={[]}
        connections={[]}
        onChanged={() => {}}
      />
    ),
  })
  const detail = createRoute({
    getParentRoute: () => root,
    path: "/clouds/$id",
    component: () => <p>Saved operation destination</p>,
  })
  setWorkspaceSlug("workspace")
  const history = createMemoryHistory({
    initialEntries: ["/workspace/changes"],
  })
  const router = createRouter({
    routeTree: root.addChildren([changes, detail]),
    history,
    rewrite: workspaceRewrite(new Set(["changes", "clouds"])),
  })
  await act(async () => {
    render(
      <ClientProvider client={forgeClient}>
        <RouterProvider router={router} />
      </ClientProvider>
    )
    await router.load()
  })
  await screen.findByRole("link", {
    name: /Change node pool: Long worker cluster/,
  })
  const pollingBefore = reads
  await act(async () => {
    vi.advanceTimersByTime(5000)
  })
  await waitFor(() => expect(reads).toBeGreaterThan(pollingBefore))
  const before = reads
  kind = "runtime-access-upgrade"
  act(() =>
    publishLiveState({
      status: "live",
      transport: "ws",
      lastSync: Date.now(),
      gap: false,
    })
  )
  const link = await screen.findByRole("link", {
    name: /Upgrade namespace access: Long worker cluster/,
  })
  expect(reads).toBeGreaterThan(before)
  expect(link).toHaveAttribute(
    "href",
    "/workspace/clouds/cloud?tab=servers&clusterOperation=op"
  )
  await userEvent.setup().click(link)
  await screen.findByText("Saved operation destination")
  await waitFor(() =>
    expect(history.location.href).toContain(
      "/workspace/clouds/cloud?tab=servers&clusterOperation=op"
    )
  )
  expect(writes).toEqual([])
})
