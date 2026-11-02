import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import type { CloudCluster, Profile } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ClusterMaintenance } from "./cluster-maintenance"
const cluster = {
  id: "cluster",
  cloudId: "cloud",
  connectionId: "conn",
  provider: "test",
  ownership: "managed",
  revision: 1,
  observedRevision: 2,
  observation: {
    name: "workers",
    ref: {
      id: "physical",
      region: "region",
      accountId: "account",
      incarnation: "inc",
    },
    pools: [
      {
        id: "pool",
        name: "workers",
        count: 2,
        size: "small",
        autoscale: false,
        min: 0,
        max: 0,
      },
    ],
  },
} as CloudCluster
const cloud = { id: "cloud", revision: 1 } as Profile
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
it("preserves desired count after stale review and sends fresh pins without provider pool IDs", async () => {
  const bodies: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith("/preview")) {
        bodies.push(JSON.parse(String(init?.body)))
        return Response.json(
          { message: "State changed. Review again." },
          { status: 409 }
        )
      }
      if (path.includes("cluster-options"))
        return Response.json({
          account_id: "account",
          connection_generation: 2,
          digest: "fresh",
          checked_at: "2026-11-01T00:00:00Z",
          node_sizes: ["small"],
        })
      if (path.includes("capacity-policy"))
        return Response.json({ policy: { revision: 3 } })
      if (path.endsWith("/clusters/cluster"))
        return Response.json({
          ...cluster,
          cloud_id: "cloud",
          observed_revision: 4,
          revision: 5,
          observation: {
            ...cluster.observation,
            ref: {
              id: "physical",
              region: "region",
              account_id: "account",
              incarnation: "inc",
            },
          },
        })
      return Response.json({ id: "cloud", revision: 6 })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ClusterMaintenance
        cluster={cluster}
        cloud={cloud}
        kind="node-pool-update"
        onClose={() => {}}
        onSaved={() => {}}
      />
    </ClientProvider>
  )
  const user = userEvent.setup()
  const count = await screen.findByRole("spinbutton", { name: "Desired nodes" })
  await user.clear(count)
  await user.type(count, "3")
  await user.click(screen.getByRole("checkbox"))
  await user.click(screen.getByRole("button", { name: "Save review" }))
  await screen.findByRole("button", { name: "Create fresh review" })
  expect(count).toHaveValue(3)
  expect(bodies[0]).toMatchObject({
    resource_revision: 5,
    cloud_revision: 6,
    maintenance: {
      pool_id: "pool",
      observed_revision: 4,
      pool: { name: "workers", count: 3, size: "small" },
    },
  })
  expect(
    (bodies[0].maintenance as { pool: { id?: string } }).pool.id
  ).toBeUndefined()
})

it("requires retained-resource inventory and acknowledgement before retirement review", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.endsWith("/retirement/preview"))
      return Response.json({
        resource_id: "cluster",
        digest: "retained-review",
        blockers: [],
        checked_at: "2026-11-01T00:00:00Z",
        estimated_remaining_hourly_micro_usd: "1000000",
        deletion_candidates: [],
        retained_resources: [
          {
            id: "volume",
            kind: "volume",
            ownership: "managed",
            survives_cluster_deletion: true,
          },
        ],
        unknown_costs: ["volume"],
      })
    if (path.includes("cluster-options"))
      return Response.json({ node_sizes: [] })
    return Response.json({ policy: { revision: 1 } })
  })
  render(
    <ClientProvider client={forgeClient}>
      <ClusterMaintenance
        cluster={cluster}
        cloud={cloud}
        kind="retire-cluster"
        onClose={() => {}}
        onSaved={() => {}}
      />
    </ClientProvider>
  )
  const user = userEvent.setup()
  const save = await screen.findByRole("button", { name: "Save review" })
  expect(save).toBeDisabled()
  await user.click(
    screen.getByRole("button", { name: "Review retained resources" })
  )
  expect(
    await screen.findByRole("table", { name: "Retirement inventory" })
  ).toHaveTextContent("Retained")
  expect(save).toBeDisabled()
  await user.click(screen.getByRole("checkbox"))
  expect(save).toBeEnabled()
  expect(
    screen.getByText(/does not delete resources or release capacity/)
  ).toBeInTheDocument()
})

it.each([
  [false, false, false],
  [false, true, true],
  [true, false, true],
])(
  "reviews optional HPA with current=%s selected=%s",
  async (currentHpa, selectHpa, expectedHpa) => {
    const bodies: { runtime_access: { profile: unknown } }[] = []
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input)
        if (path.endsWith("/preview")) {
          bodies.push(JSON.parse(String(init?.body)))
          return Response.json({ id: "review", status: "preview" })
        }
        if (path.endsWith("/attachments"))
          return Response.json({
            cluster_id: "cluster",
            items: [
              {
                id: "attachment",
                revision: 2,
                profile: {
                  schema_version: 1,
                  cleanup_dependents: true,
                  hpa: currentHpa,
                },
              },
            ],
          })
        if (path.includes("cluster-options"))
          return Response.json({
            account_id: "account",
            connection_generation: 1,
            node_sizes: [],
          })
        if (path.includes("capacity-policy"))
          return Response.json({ policy: { revision: 1 } })
        if (path.endsWith("/clusters/cluster"))
          return Response.json({
            ...cluster,
            cloud_id: "cloud",
            connection_id: "conn",
          })
        return Response.json({ id: "cloud", revision: 1 })
      }
    )
    render(
      <ClientProvider client={forgeClient}>
        <ClusterMaintenance
          cluster={cluster}
          cloud={cloud}
          kind="runtime-access-upgrade"
          attachmentId="attachment"
          onClose={() => {}}
          onSaved={() => {}}
        />
      </ClientProvider>
    )
    const user = userEvent.setup()
    const hpa = await screen.findByRole("checkbox", {
      name: /Allow horizontal scaling/,
    })
    expect(hpa).not.toBeChecked()
    if (selectHpa) await user.click(hpa)
    await user.click(
      screen.getByRole("checkbox", {
        name: /Review the requested namespace permissions/,
      })
    )
    await user.click(screen.getByRole("button", { name: "Save review" }))
    await screen.findByRole("button", { name: "Save review" })
    await waitFor(() =>
      expect(bodies[0]?.runtime_access.profile).toEqual({
        schema_version: 1,
        cleanup_dependents: true,
        hpa: expectedHpa,
      })
    )
  }
)
