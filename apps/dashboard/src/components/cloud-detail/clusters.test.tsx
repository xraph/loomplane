import { ClientProvider } from "@forge-go/client-react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { publishLiveState } from "@/lib/realtime-state"
import { forgeClient } from "@/client/runtime"
import type { Profile } from "@/client/forge/loomplane/types"
import { ClusterNodePools } from "./cluster-node-pools"
import { Clusters } from "./clusters"
import { ClusterDetail, ClusterOperationDetail } from "./cluster-detail"
vi.mock("@/lib/router", () => ({
  Link: ({
    params,
    search,
    children,
    ...props
  }: {
    params: { id: string }
    search: { tab: string }
    children: React.ReactNode
  }) => (
    <a {...props} href={`/clouds/${params.id}?tab=${search.tab}`}>
      {children}
    </a>
  ),
}))
const permissions = vi.hoisted(() => ({
  allowed: true,
  denied: [] as string[],
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (permission: string) =>
      permissions.allowed && !permissions.denied.includes(permission),
  }),
}))
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  publishLiveState({
    status: "stale",
    transport: "ws",
    lastSync: 0,
    gap: false,
  })
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.allowed = true
  permissions.denied = []
  window.history.replaceState(null, "", "/")
})
const cloud = { id: "cloud", name: "Production", revision: 1 } as Profile
const mount = (child: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{child}</ClientProvider>)
it("distinguishes empty, denied and failed cluster reads", async () => {
  const fetcher = vi.fn().mockResolvedValue(Response.json({ items: [] }))
  vi.stubGlobal("fetch", fetcher)
  const view = mount(<Clusters cloud={cloud} />)
  expect(
    await screen.findByText("No clusters in this Cloud")
  ).toBeInTheDocument()
  view.unmount()
  forgeClient.clear()
  fetcher.mockImplementation(() =>
    Promise.resolve(Response.json({ message: "denied" }, { status: 403 }))
  )
  const denied = mount(<Clusters cloud={cloud} />)
  expect(
    await screen.findByText("Cluster access is restricted")
  ).toBeInTheDocument()
  denied.unmount()
  forgeClient.clear()
  fetcher.mockImplementation(() =>
    Promise.resolve(
      Response.json({ message: "provider unavailable" }, { status: 500 })
    )
  )
  mount(<Clusters cloud={cloud} />)
  expect(await screen.findByText(/Provider unavailable/)).toBeInTheDocument()
  expect(
    screen.queryByText("No clusters in this Cloud")
  ).not.toBeInTheDocument()
})
it("project callers never request account cluster lifecycle", () => {
  permissions.allowed = false
  const fetcher = vi.fn()
  vi.stubGlobal("fetch", fetcher)
  mount(<Clusters cloud={cloud} />)
  expect(
    screen.getByText("Cluster management is restricted")
  ).toBeInTheDocument()
  expect(fetcher).not.toHaveBeenCalled()
})
const operation = {
  id: "op",
  cloud_id: "cloud",
  status: "preview",
  digest: "exact",
  revision: 1,
  kind: "create",
  input: {
    spec: {
      name: "workers",
      node_pools: [
        { name: "workers", size: "s-2vcpu-4gb", count: 3, autoscale: false },
      ],
    },
    ref: {},
    provider: "digitalocean",
    attachment: { project_id: "project" },
  },
  steps: [],
  unknown_costs: ["load balancers"],
  estimated_node_hourly_micro_usd: 100000,
}
it("blocks duplicate apply and preserves a stale-review recovery path", async () => {
  let apply = 0
  let finish: (response: Response) => void = () => undefined
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("/apply")) {
      apply++
      expect(JSON.parse(String(init?.body))).toEqual({ review: "exact" })
      return new Promise<Response>((resolve) => {
        finish = resolve
      })
    }
    return Promise.resolve(Response.json(operation))
  })
  const refresh = vi.fn()
  mount(
    <ClusterOperationDetail
      cloud={cloud}
      id="op"
      onBack={() => undefined}
      onRefreshReview={refresh}
    />
  )
  const user = userEvent.setup()
  const button = await screen.findByRole("button", {
    name: "Apply reviewed change",
  })
  await user.dblClick(button)
  expect(apply).toBe(1)
  finish(Response.json({ message: "stale review" }, { status: 409 }))
  await user.click(
    await screen.findByRole("button", { name: "Refresh options and review" })
  )
  expect(refresh).toHaveBeenCalledOnce()
})
it("reload reads the saved operation without another create", async () => {
  window.history.replaceState(null, "", "/?clusterOperation=op")
  const fetcher = vi.fn((input: RequestInfo | URL) =>
    Promise.resolve(
      Response.json(
        String(input).includes("cluster-operations")
          ? {
              ...operation,
              status: "needs_attention",
              error: "runtime_setup_incomplete",
            }
          : { items: [] }
      )
    )
  )
  vi.stubGlobal("fetch", fetcher)
  mount(<Clusters cloud={cloud} />)
  expect(await screen.findByText("Setup incomplete")).toBeInTheDocument()
  await waitFor(() =>
    expect(
      fetcher.mock.calls.some(([input]) =>
        String(input).includes("cluster-operations/op")
      )
    ).toBe(true)
  )
  expect(
    fetcher.mock.calls.every(([input]) => !String(input).includes("preview"))
  ).toBe(true)
})

it("clears recovery guidance when a cached operation succeeds live", async () => {
  let succeeded = false
  vi.stubGlobal("fetch", () =>
    Promise.resolve(
      Response.json(
        succeeded
          ? { ...operation, status: "succeeded", revision: 2 }
          : {
              ...operation,
              status: "needs_attention",
              error: "operator_recovery_required",
            }
      )
    )
  )
  mount(
    <ClusterOperationDetail
      cloud={cloud}
      id="op"
      onBack={() => undefined}
      onRefreshReview={() => undefined}
    />
  )
  await screen.findByText(/Provider resources may still exist/)
  succeeded = true
  act(() =>
    publishLiveState({
      status: "live",
      transport: "ws",
      lastSync: 123,
      gap: false,
    })
  )
  await screen.findByText("Succeeded")
  expect(
    screen.queryByText(/Provider resources may still exist/)
  ).not.toBeInTheDocument()
})

it("shows saved fixed node pool scope before applying after reload", async () => {
  window.history.replaceState(null, "", "/?clusterOperation=op")
  vi.stubGlobal("fetch", (input: RequestInfo | URL) =>
    Promise.resolve(
      Response.json(
        String(input).includes("cluster-operations") ? operation : { items: [] }
      )
    )
  )
  mount(<Clusters cloud={cloud} />)
  await screen.findByRole("button", { name: "Apply reviewed change" })
  expect(screen.getByText(/workers: 3 × s-2vcpu-4gb/)).toHaveTextContent(
    "Fixed capacity"
  )
})

it("loads current cluster by ID and exposes permission-specific controls", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) =>
      Promise.resolve(
        Response.json(
          String(input).endsWith("/attachments")
            ? { items: [] }
            : String(input).endsWith("/capacity")
              ? { nodes: null, conditions: null, complete: false }
              : {
                  id: "cluster",
                  cloud_id: "cloud",
                  ownership: "managed",
                  status: "running",
                  external_id: "provider-cluster",
                  provider: "digitalocean",
                  observation: {
                    ref: { region: "nyc1" },
                    pools: [
                      {
                        id: "pool",
                        name: "workers",
                        size: "s-2vcpu-4gb",
                        count: 3,
                      },
                    ],
                  },
                }
        )
      )
    )
  )
  mount(
    <ClusterDetail
      cloud={cloud}
      id="cluster"
      onSaved={() => {}}
      onBack={() => {}}
    />
  )
  expect(await screen.findByText("Observed 3 nodes")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Runtime targets" })).toHaveAttribute(
    "href",
    "/clouds/cloud?tab=targets"
  )
  expect(
    screen.getByText(/History grants no standing permission/)
  ).toBeInTheDocument()
})

it("keeps inventory visible while specific lifecycle permissions are denied", async () => {
  permissions.denied = ["cluster:manage", "cluster:delete"]
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) =>
      Promise.resolve(
        Response.json(
          String(input).endsWith("/attachments")
            ? {
                items: [
                  {
                    id: "attachment",
                    namespace: "workers",
                    revision: 8,
                    state: "active",
                  },
                ],
              }
            : String(input).endsWith("/capacity")
              ? { nodes: null, conditions: null, complete: false }
              : {
                  id: "cluster",
                  cloud_id: "cloud",
                  ownership: "managed",
                  status: "running",
                  external_id: "provider-cluster",
                  provider: "digitalocean",
                  observation: { ref: { region: "nyc1" }, pools: [] },
                }
        )
      )
    )
  )
  mount(
    <ClusterDetail
      cloud={cloud}
      id="cluster"
      onSaved={() => {}}
      onBack={() => {}}
    />
  )
  await screen.findByText("provider-cluster")
  await screen.findByText("workers")
  expect(screen.queryByRole("button", { name: "Change nodes" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Review retirement" })).toBeNull()
  expect(
    screen.queryByRole("button", { name: "Review access upgrade" })
  ).toBeNull()
})
it.each([
  "pdb_blocked",
  "unknown_price",
  "stale_price",
  "unknown_quota",
  "observation_drift",
  "runtime_access_upgrade_required",
])(
  "shows saved %s blockers after reload without a raw bypass",
  async (reason) => {
    vi.stubGlobal("fetch", async () =>
      Response.json({
        ...operation,
        kind: "node-pool-update",
        status: "needs_attention",
        error: reason,
      })
    )
    mount(
      <ClusterOperationDetail
        cloud={cloud}
        id="op"
        onBack={() => {}}
        onRefreshReview={() => {}}
      />
    )
    const retry = await screen.findByRole("button", {
      name: "Retry saved operation",
    })
    if (reason === "stale_price") {
      expect(retry).toBeDisabled()
      expect(
        screen.getByRole("button", { name: "Refresh options and review" })
      ).toBeEnabled()
    }
    expect(screen.queryByRole("button", { name: /force|bypass/i })).toBeNull()
    expect(screen.getByRole("status")).toHaveTextContent(
      reason === "runtime_access_upgrade_required"
        ? "separately reviewed access upgrade"
        : reason === "stale_price"
          ? "Prices are out of date"
          : reason.replaceAll("_", " ")
    )
  }
)

const currentCluster = {
  id: "cluster",
  cloudId: "cloud",
  connectionId: "connection",
  provider: "test",
  ownership: "managed",
  revision: 2,
  observedRevision: 3,
  observation: {
    name: "physical",
    ref: { id: "physical", region: "region" },
    pools: [
      {
        id: "pool",
        name: "workers",
        size: "small",
        count: 1,
        autoscale: true,
        min: 1,
        max: 2,
      },
    ],
  },
} as unknown as import("@/client/forge/loomplane/types").CloudCluster

it("reads persisted capacity after reconnect and ages freshness without provider writes", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] })
  vi.setSystemTime(new Date("2026-11-01T12:00:00Z"))
  let revision = 1
  let reads = 0
  const writes: string[] = []
  const checked = new Date().toISOString()
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method && init.method !== "GET") writes.push(String(input))
      reads++
      return Response.json({
        resource_id: "cluster",
        observation_revision: revision,
        checked_at: checked,
        fresh: true,
        complete: true,
        workloads_complete: true,
        nodes: [],
        conditions: [
          {
            type: "fixture",
            reason:
              revision === 1 ? "first observation" : "updated observation",
          },
        ],
      })
    }
  )
  mount(
    <ClusterNodePools cluster={currentCluster} canManage onChange={() => {}} />
  )
  await screen.findByText(/first observation/)
  const before = reads
  revision = 2
  act(() =>
    publishLiveState({
      status: "live",
      transport: "ws",
      lastSync: Date.now(),
      gap: false,
    })
  )
  await screen.findByText(/updated observation/)
  expect(reads).toBeGreaterThan(before)
  expect(screen.getByText(/^Fresh ·/)).toBeInTheDocument()
  await act(async () => {
    vi.advanceTimersByTime(121000)
  })
  expect(screen.getByText(/^Stale ·/)).toBeInTheDocument()
  expect(writes).toEqual([])
})

it("shows observed native bounds separately while reviewed bounds await convergence", async () => {
  vi.stubGlobal("fetch", async () =>
    Response.json({ nodes: [], conditions: [], checked_at: "", fresh: false })
  )
  mount(
    <ClusterNodePools
      cluster={{
        ...currentCluster,
        scalingPolicies: {
          pool: { mode: "native", min_nodes: 2, max_nodes: 3 },
        },
      }}
      canManage
      onChange={() => {}}
    />
  )
  expect(
    screen.getByText(/Observed 1 nodes · bounds 1 to 2/)
  ).toBeInTheDocument()
  expect(
    screen.getByText(
      /Reviewed native · bounds 2 to 3 · Awaiting provider convergence/
    )
  ).toBeInTheDocument()
})

it("retries the exact saved operation after reload", async () => {
  const writes: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") writes.push(String(input))
      return Response.json({
        ...operation,
        status: writes.length ? "accepted" : "needs_attention",
        error: "pdb_blocked",
      })
    }
  )
  mount(
    <ClusterOperationDetail
      cloud={cloud}
      id="op"
      onBack={() => {}}
      onRefreshReview={() => {}}
    />
  )
  await userEvent
    .setup()
    .click(await screen.findByRole("button", { name: "Retry saved operation" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatch(/cluster-operations\/op\/retry$/)
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "Retry saved operation" })
    ).toBeNull()
  )
})

it("shows changed retirement inventory and clears acknowledgement before saving a replacement review", async () => {
  const previews: unknown[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith("/apply"))
        return Response.json({ message: "State changed" }, { status: 409 })
      if (path.endsWith("/retirement/preview"))
        return Response.json({
          digest: "new-inventory",
          checked_at: new Date().toISOString(),
          blockers: [],
          deletion_candidates: [],
          retained_resources: [
            {
              id: "new-volume",
              kind: "volume",
              ownership: "managed",
              survives_cluster_deletion: true,
            },
          ],
          unknown_costs: ["storage"],
        })
      if (path.endsWith("/clusters/preview")) {
        previews.push(JSON.parse(String(init?.body)))
        return Response.json({ id: "replacement" })
      }
      if (path.endsWith("/attachments"))
        return Response.json({ items: [{ id: "attachment", revision: 8 }] })
      if (path.endsWith("/clusters/cluster"))
        return Response.json({
          ...currentCluster,
          cloud_id: "cloud",
          connection_id: "connection",
        })
      if (path.includes("cluster-options"))
        return Response.json({
          node_sizes: [],
          account_id: "account",
          connection_generation: 1,
        })
      if (path.includes("capacity-policy"))
        return Response.json({ policy: { revision: 1 } })
      if (path.includes("/cluster-operations/"))
        return Response.json({
          ...operation,
          resource_id: "cluster",
          kind: "detach-runtime",
          input: {
            ...operation.input,
            retirement: {
              attachment_id: "attachment",
              expected_attachment_revision: 1,
              review_digest: "old-inventory",
            },
            acknowledge_unknown_costs: true,
          },
        })
      return Response.json({ id: "cloud", revision: 2 })
    }
  )
  const saved = vi.fn()
  mount(
    <ClusterOperationDetail
      cloud={cloud}
      id="op"
      onBack={() => {}}
      onRefreshReview={() => {}}
      onSaved={saved}
    />
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "Apply reviewed change" })
  )
  await user.click(
    await screen.findByRole("button", { name: "Refresh options and review" })
  )
  const save = await screen.findByRole("button", { name: "Save review" })
  expect(save).toBeDisabled()
  expect(previews).toEqual([])
  await user.click(
    screen.getByRole("button", { name: "Review retained resources" })
  )
  await screen.findByText("volume · new-volume")
  expect(save).toBeDisabled()
  await user.click(screen.getByRole("checkbox", { name: "Acknowledge review" }))
  expect(save).toBeEnabled()
  await user.click(
    screen.getByRole("button", { name: "Review retained resources" })
  )
  await waitFor(() =>
    expect(
      screen.getByRole("checkbox", { name: "Acknowledge review" })
    ).not.toBeChecked()
  )
  expect(save).toBeDisabled()
  expect(previews).toEqual([])
  await user.click(screen.getByRole("checkbox", { name: "Acknowledge review" }))
  await user.click(save)
  await waitFor(() => expect(saved).toHaveBeenCalledWith("replacement"))
  expect(previews[0]).toMatchObject({
    retirement: {
      review_digest: "new-inventory",
      expected_attachment_revision: 8,
    },
  })
})
