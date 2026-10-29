import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import type { Profile } from "@/client/forge/loomplane/types"
import { ClusterCreate } from "./cluster-create"
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
it("reviews four stages and sends provider account identity through the generated wire contract", async () => {
  let submitted: Record<string, unknown> | undefined
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: "conn",
              cloud: "digitalocean",
              name: "Disposable account",
              status: "ready",
              generation: 2,
              account_identity: "generic-hash",
              details: {},
            },
          ],
        })
      if (path.endsWith("/providers"))
        return Response.json({
          providers: [
            {
              cloud: "digitalocean",
              available: true,
              infrastructure: ["cluster:discover", "cluster:create"],
            },
          ],
        })
      if (path.includes("cluster-options"))
        return Response.json({
          account_id: "provider-account",
          connection_generation: 7,
          digest: "options",
          checked_at: "2026-10-29T00:00:00Z",
          regions: ["nyc1"],
          versions: ["1.36.4"],
          networks: ["network"],
          node_sizes: ["small"],
          node_size_facts: [
            {
              id: "small",
              cpus: 2,
              memory_mib: 4096,
              disk_gb: 80,
              hourly_usd: 0.1,
            },
          ],
        })
      if (path.endsWith("/clouds/cloud/projects"))
        return Response.json({
          grants: [
            {
              project_id: "project",
              revision: 3,
              revoked: false,
              target_ids: [],
            },
          ],
        })
      if (path.endsWith("/projects"))
        return Response.json({
          projects: [{ id: "project", name: "Worker project" }],
        })
      if (path.endsWith("/capacity-policy"))
        return Response.json({ policy: { revision: 4 } })
      if (path.endsWith("/preview")) {
        submitted = JSON.parse(String(init?.body))
        return Response.json({ id: "op" })
      }
      return Response.json({ items: [] })
    }
  )
  const saved = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <ClusterCreate
        cloud={{ id: "cloud", revision: 1 } as Profile}
        onClose={() => undefined}
        onSaved={saved}
      />
    </ClientProvider>
  )
  const user = userEvent.setup()
  const choose = async (label: string, option: string) => {
    await user.click(await screen.findByRole("combobox", { name: label }))
    await user.click(await screen.findByRole("option", { name: option }))
  }
  await choose("Provider account", "Disposable account")
  await choose("Region", "nyc1")
  await user.click(screen.getByRole("button", { name: "Continue" }))
  await user.type(screen.getByLabelText("Cluster name"), "workers")
  await choose("Kubernetes version", "1.36.4")
  await choose("Network", "network")
  await choose("Node size", "small")
  await user.click(screen.getByRole("button", { name: "Continue" }))
  await choose("Project runtime grant", "Worker project")
  await user.type(
    screen.getByLabelText("New namespace and target slug"),
    "workers"
  )
  const image = "registry/worker@sha256:" + "a".repeat(64)
  await user.type(screen.getByLabelText("Digest-pinned worker image"), image)
  await user.click(screen.getByRole("button", { name: "Continue" }))
  expect(screen.getByText(`Worker image: ${image}`)).toBeInTheDocument()
  expect(
    screen.getByText(/Kubernetes 1.36.4. Network network/)
  ).toBeInTheDocument()
  await user.click(screen.getByRole("checkbox"))
  await user.click(screen.getByRole("button", { name: "Save review" }))
  expect(submitted).toMatchObject({
    account_id: "provider-account",
    connection_generation: 7,
    policy_revision: 4,
    attachment: {
      grant_revision: 3,
      project_id: "project",
      namespace: "workers",
      prerequisites: {
        require_builds: false,
        require_ingress: false,
        require_persistent_storage: false,
        require_tls: false,
      },
    },
  })
  expect(saved).toHaveBeenCalledWith("op")
}, 20000)
