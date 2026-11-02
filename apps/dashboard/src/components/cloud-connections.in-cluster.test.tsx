import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ConnectDialog } from "./cloud-connections"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

type Posted = { path: string; body: Record<string, unknown> }

const placement = [
  { key: "storage_class", label: "Retained storage class (optional)" },
]

function kubernetes(methods: string[]) {
  return {
    cloud: "kubernetes",
    title: "Kubernetes",
    builtin: false,
    credential: "Self-contained kubeconfig",
    methods,
    settings: [
      { key: "namespace", label: "Namespace (defaults to kubeconfig)" },
      ...placement,
    ],
    in_cluster_settings: [
      { key: "namespace", label: "Namespace", required: true },
      ...placement,
    ],
  }
}

const connection = {
  id: "conn_c1",
  generation: 1,
  org_id: "org_1",
  cloud: "kubernetes",
  name: "This cluster",
  method: "in_cluster",
  account: "in-cluster/apps",
  details: { namespace: "apps" },
  status: "ready",
  missing: [],
  message: "",
  checked_at: null,
  created_by: "u1",
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
}

function stub(methods: string[], posted: Posted[] = []) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST") {
        posted.push({
          path,
          body: JSON.parse(String(init.body ?? "{}")) as Record<
            string,
            unknown
          >,
        })
        return Response.json(connection, { status: 201 })
      }
      if (path.includes("connection-clouds"))
        return Response.json({ clouds: [kubernetes(methods)] })
      return Response.json({ connections: [] })
    })
  )
}

function dialog() {
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud="kubernetes" onOpenChange={vi.fn()} />
    </ClientProvider>
  )
}

it("connects the cluster Loomplane runs in without a kubeconfig", async () => {
  const posted: Posted[] = []
  stub(["in_cluster", "token"], posted)
  dialog()
  const namespace = await screen.findByLabelText("Namespace")
  expect(screen.queryByLabelText("Self-contained kubeconfig")).toBeNull()
  // A label that already says optional isn't told so twice.
  expect(
    screen.getByLabelText("Retained storage class (optional)")
  ).toBeTruthy()

  const user = userEvent.setup()
  await user.type(screen.getByLabelText("Name"), "This cluster")
  await user.type(namespace, "apps")
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0].path).toMatch(/\/api\/v1\/connections\/in-cluster$/)
  expect(posted[0].body).toEqual({
    name: "This cluster",
    settings: { namespace: "apps" },
  })
})

it("switches between this cluster and a kubeconfig", async () => {
  stub(["in_cluster", "token"])
  dialog()
  const user = userEvent.setup()
  await user.click(await screen.findByText("Use a kubeconfig instead"))
  expect(screen.getByLabelText("Self-contained kubeconfig")).toBeTruthy()
  await user.click(
    screen.getByText("Connect the cluster Loomplane runs in instead")
  )
  expect(screen.queryByLabelText("Self-contained kubeconfig")).toBeNull()
  expect(screen.getByLabelText("Namespace")).toBeTruthy()
})

it("asks for a kubeconfig where Loomplane can't connect its own cluster", async () => {
  stub(["token"])
  dialog()
  expect(await screen.findByLabelText("Self-contained kubeconfig")).toBeTruthy()
  expect(
    screen.queryByText("Connect the cluster Loomplane runs in instead")
  ).toBeNull()
})
