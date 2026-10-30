import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ClientProvider } from "@forge-go/client-react"
import userEvent from "@testing-library/user-event"
import { forgeClient } from "@/client/runtime"
import { ConnectDialog } from "./cloud-connections"
import type { Connection } from "@/client/forge/loomplane/types"
import { ClusterBuildSetup, clusterSetupWaiting } from "./cluster-build-setup"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const connection: Connection = {
  id: "conn_test",
  name: "Local",
  orgId: "org_test",
  account: "local/apps",
  method: "token",
  generation: 1,
  message: "",
  createdBy: "user_test",
  createdAt: "2026-10-29T00:00:00Z",
  updatedAt: "2026-10-29T00:00:00Z",
  cloud: "kubernetes",
  status: "incomplete",
  details: {
    registry_hostname: "images.example.com",
    registry_setup_status: "waiting",
  },
  missing: [
    "cluster setup: waiting for the managed ingress load-balancer address",
  ],
}

it("shows pending infrastructure without asking for IAM roles", () => {
  render(<ClusterBuildSetup connection={connection} />)
  expect(clusterSetupWaiting(connection)).toBe(true)
  expect(screen.getByText("Waiting for cluster setup")).toBeTruthy()
  expect(screen.getByText(connection.missing[0])).toBeTruthy()
  expect(screen.queryByText(/Grant .*role/)).toBeNull()
})

it("shows the discovered DNS destination and keeps permission gaps visible", () => {
  render(
    <ClusterBuildSetup
      connection={{
        ...connection,
        details: {
          ...connection.details,
          registry_setup_status: "ready",
          ingress_address: "203.0.113.12",
        },
        missing: ["pods/exec:create"],
      }}
    />
  )
  expect(screen.getByText("203.0.113.12")).toBeTruthy()
  expect(screen.getByText("images.example.com")).toBeTruthy()
  expect(screen.getByText("Cluster requirements")).toBeTruthy()
  expect(screen.getByText("pods/exec:create")).toBeTruthy()
})

it("does not show a waiting alert for a ready connection", () => {
  const ready = { ...connection, status: "ready", missing: [] }
  render(<ClusterBuildSetup connection={ready} />)
  expect(clusterSetupWaiting(ready)).toBe(false)
  expect(screen.queryByRole("alert")).toBeNull()
})

it("connects automatic setup with a hostname and no placement overrides", async () => {
  const posted: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST") {
        posted.push(JSON.parse(String(init.body)))
        return Response.json(
          {
            ...connection,
            id: "conn_test",
            name: "Local",
            cloud: "kubernetes",
          },
          { status: 201 }
        )
      }
      if (path.includes("connection-clouds"))
        return Response.json({
          clouds: [
            {
              cloud: "kubernetes",
              title: "Kubernetes",
              builtin: false,
              credential: "Self-contained kubeconfig",
              methods: ["token"],
              settings: [
                { key: "registry_hostname", label: "Registry hostname" },
              ],
            },
          ],
        })
      return Response.json({ connections: [] })
    })
  )
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud="kubernetes" onOpenChange={vi.fn()} />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.type(
    await screen.findByLabelText(/Registry hostname/),
    "images.example.com"
  )
  await user.type(screen.getByLabelText("Name"), "Local")
  await user.type(
    screen.getByLabelText("Self-contained kubeconfig"),
    "test-kubeconfig"
  )
  await user.click(screen.getByRole("button", { name: "Set up and connect" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0].settings).toEqual({
    registry_hostname: "images.example.com",
  })
})
