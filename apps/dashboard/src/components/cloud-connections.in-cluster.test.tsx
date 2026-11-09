import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import type { Connection } from "@/client/forge/loomplane/types"
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

function kubernetes(
  methods: string[],
  extra: { key: string; label: string; required?: boolean }[] = []
) {
  return {
    cloud: "kubernetes",
    title: "Kubernetes",
    builtin: false,
    credential: "Self-contained kubeconfig",
    methods,
    settings: [
      { key: "namespace", label: "Namespace (defaults to kubeconfig)" },
      ...placement,
      ...extra,
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

function stub(
  methods: string[],
  posted: Posted[] = [],
  extra: { key: string; label: string; required?: boolean }[] = []
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST" || init?.method === "PUT") {
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
        return Response.json({ clouds: [kubernetes(methods, extra)] })
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
  // The dialog asks what the page does: Kubernetes has a form of its own.
  expect(
    await screen.findByRole("radio", { name: "The cluster Loomplane runs in" })
  ).toHaveProperty("ariaChecked", "true")
  expect(screen.queryByLabelText("Kubeconfig")).toBeNull()

  const user = userEvent.setup()
  await user.type(screen.getByLabelText("Name"), "This cluster")
  await user.type(screen.getByLabelText("Namespace"), "apps")
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
  await user.click(
    await screen.findByRole("radio", { name: "With a kubeconfig" })
  )
  expect(screen.getByLabelText("Kubeconfig")).toBeTruthy()
  await user.click(
    screen.getByRole("radio", { name: "The cluster Loomplane runs in" })
  )
  expect(screen.queryByLabelText("Kubeconfig")).toBeNull()
  expect(screen.getByLabelText("Namespace")).toBeTruthy()
})

it("asks for a kubeconfig where Loomplane can't connect its own cluster", async () => {
  stub(["token"])
  dialog()
  expect(await screen.findByLabelText("Kubeconfig")).toBeTruthy()
  expect(screen.queryByText("The cluster Loomplane runs in")).toBeNull()
})

it("gives a setting the form doesn't know a field of its own", async () => {
  stub(
    ["token"],
    [],
    [{ key: "node_pool", label: "Node pool", required: true }]
  )
  dialog()
  expect(await screen.findByLabelText("Node pool")).toBeTruthy()
})

it("replaces a cluster's credential and keeps its settings", async () => {
  const posted: Posted[] = []
  stub(["in_cluster", "token"], posted)
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog
        open
        cloud="kubernetes"
        replacing={
          {
            ...connection,
            method: "token",
            details: { namespace: "apps", storage_class: "fast" },
          } as unknown as Connection
        }
        onOpenChange={vi.fn()}
      />
    </ClientProvider>
  )
  const kubeconfig = await screen.findByLabelText("Kubeconfig")
  // It keeps its name, and a preset would start its settings over.
  expect(screen.queryByLabelText("Name")).toBeNull()
  expect(screen.queryByText("Start from")).toBeNull()
  expect(screen.getByLabelText("Namespace")).toHaveProperty("value", "apps")

  const user = userEvent.setup()
  await user.click(kubeconfig)
  await user.paste("apiVersion: v1")
  await user.click(screen.getByRole("button", { name: "Check and replace" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0].path).toMatch(/\/connections\/conn_c1\/credential$/)
  expect(posted[0].body).toEqual({
    credential: "apiVersion: v1",
    settings: { namespace: "apps", storage_class: "fast" },
  })
})
