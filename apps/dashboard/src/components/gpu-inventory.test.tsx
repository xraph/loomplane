import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { GpuInventory } from "@/components/gpu-inventory"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function stub(inventory: Record<string, unknown>, status = 200) {
  vi.stubGlobal("fetch", async () => {
    return new Response(
      JSON.stringify(
        status === 200
          ? { checked_at: "2026-10-25T12:00:00Z", ...inventory }
          : inventory
      ),
      { status, headers: { "content-type": "application/json" } }
    )
  })
}
function mount(cloud: "server" | "kubernetes") {
  return render(
    <ClientProvider client={forgeClient}>
      <GpuInventory connectionId="connection" cloud={cloud} />
    </ClientProvider>
  )
}
const l4 = {
  index: 0,
  uuid: "GPU-a",
  kind: "nvidia-l4",
  product: "NVIDIA L4",
  memory_mib: 23034,
}

it("lists a server's GPUs", async () => {
  stub({ status: "ready", devices: [l4] })
  mount("server")
  expect(await screen.findByText("NVIDIA L4")).toBeInTheDocument()
  expect(screen.getByText("22.5 GiB")).toBeInTheDocument()
})
it("says a cluster has no GPU nodes and links the docs", async () => {
  stub({ status: "none" })
  mount("kubernetes")
  expect(await screen.findByText("No GPU nodes")).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "GPU prerequisites" })
  ).toHaveAttribute("href", "https://loomplane.dev/docs/clouds/gpus")
})
it("explains a cluster whose nodes Loomplane can't list", async () => {
  stub({
    status: "unlisted",
    message: "Loomplane can't list this cluster's nodes, so it can't see GPUs.",
  })
  mount("kubernetes")
  expect(
    await screen.findByText("Loomplane can't list this cluster's nodes")
  ).toBeInTheDocument()
  expect(screen.queryByText("No GPU nodes")).not.toBeInTheDocument()
})
it("shows why a server can't take GPU work", async () => {
  stub({
    status: "toolkit_missing",
    message: "Install the NVIDIA Container Toolkit",
    devices: [l4],
  })
  mount("server")
  expect(
    await screen.findByText(/Install the NVIDIA Container Toolkit/)
  ).toBeInTheDocument()
})
it("shows the inventory's own message when the GPU kind is unknown", async () => {
  stub({
    status: "unknown",
    message: "GPUs are allocatable but no node label says which kind.",
  })
  mount("kubernetes")
  expect(
    await screen.findByText(/no node label says which kind/)
  ).toBeInTheDocument()
  expect(
    screen.queryByText(/check the connection again/i)
  ).not.toBeInTheDocument()
})
it("shows a server with no GPUs as one muted line", async () => {
  stub({ status: "none" })
  mount("server")
  expect(await screen.findByText("No GPUs on this server.")).toBeInTheDocument()
})
it("shows cluster node groups with an estimated price per node", async () => {
  stub({
    status: "ready",
    nodes: [
      {
        kind: "nvidia-l4",
        memory_gib: 24,
        nodes: 2,
        per_node: 1,
        total: 2,
        instance_type: "g2-standard-4",
        hourly_usd: 0.71,
        hourly_price_known: true,
      },
    ],
  })
  mount("kubernetes")
  expect(await screen.findByText("NVIDIA L4")).toBeInTheDocument()
  expect(screen.getByText("$0.71/hr per node, estimate")).toBeInTheDocument()
})
it("shows an error with a retry when the read fails", async () => {
  stub({ error: "GPUs could not be read" }, 503)
  mount("server")
  expect(
    await screen.findByRole("button", { name: "Retry" })
  ).toBeInTheDocument()
})
