import { ClientProvider } from "@forge-go/client-react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { publishLiveState } from "@/lib/realtime-state"
import { forgeClient } from "@/client/runtime"
import type { CloudCluster, Profile } from "@/client/forge/loomplane/types"
import { Clusters } from "./clusters"
import { ClusterDetail, ClusterOperationDetail } from "./cluster-detail"
const permissions = vi.hoisted(() => ({ allowed: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => permissions.allowed }),
}))
afterEach(() => {
  cleanup()
  publishLiveState({
    status: "stale",
    transport: "ws",
    lastSync: 0,
    gap: false,
  })
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.allowed = true
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
    name: "Apply reviewed setup",
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
  await screen.findByRole("button", { name: "Apply reviewed setup" })
  expect(screen.getByText(/workers: 3 × s-2vcpu-4gb/)).toHaveTextContent(
    "Fixed capacity"
  )
})

it("shows observed pools and navigable setup and Cloud targets", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      Response.json({
        items: [
          {
            ...operation,
            resource_id: "cluster",
            input: {
              ...operation.input,
              attachment: { project_id: "project" },
            },
          },
        ],
      })
    )
  )
  mount(
    <ClusterDetail
      cluster={
        {
          id: "cluster",
          cloudId: "cloud",
          status: "running",
          externalId: "provider-cluster",
          provider: "digitalocean",
          observation: {
            ref: { region: "nyc1" },
            pools: [{ name: "workers", size: "s-2vcpu-4gb", count: 3 }],
          },
        } as CloudCluster
      }
      onBack={() => {}}
    />
  )
  expect(screen.getByText("3 × s-2vcpu-4gb")).toBeInTheDocument()
  expect(
    await screen.findByRole("link", { name: "View setup operation" })
  ).toHaveAttribute("href", "/clouds/cloud?tab=servers&clusterOperation=op")
  expect(
    screen.getByRole("link", { name: "View Cloud targets" })
  ).toHaveAttribute("href", "/clouds/cloud?tab=targets")
})
