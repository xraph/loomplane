import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { GpuTargets, InfrastructureGpus } from "@/components/gpu-targets"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const l4 = { count: 1, kind: "nvidia-l4", memory_gib: 24, vendor: "nvidia" }
const h100 = {
  count: 1,
  kind: "nvidia-h100-80gb",
  memory_gib: 80,
  vendor: "nvidia",
}
const cluster = {
  source: "connection",
  connection_id: "k8s",
  name: "gpu-cluster",
  cloud: "kubernetes",
  accelerator: l4,
  free: 3,
  fits: true,
  hourly_usd: 0.71,
  hourly_price_known: true,
  price_estimate: true,
}
const busyBox = {
  source: "connection",
  connection_id: "box",
  name: "gpu-box",
  cloud: "server",
  accelerator: l4,
  free: 0,
  fits: false,
  code: "gpu_unavailable",
  message: "All of this server's NVIDIA L4 GPUs are in use.",
  hourly_usd: 0,
  hourly_price_known: false,
  price_estimate: false,
}
const size = {
  source: "server_size",
  connection_id: "do",
  name: "gpu-h100x1-80gb",
  size_id: "gpu-h100x1-80gb",
  cloud: "digitalocean",
  regions: ["tor1"],
  accelerator: h100,
  free: 1,
  fits: true,
  hourly_usd: 3.39,
  hourly_price_known: true,
  price_estimate: false,
}

function stub(
  targets: Record<string, unknown>[],
  unreachable: Record<string, unknown>[] = []
) {
  const seen: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    seen.push(url.pathname + url.search)
    if (url.pathname.endsWith("/gpu-targets"))
      return Response.json({ targets, unreachable })
    if (url.pathname.endsWith("/connections"))
      return Response.json({
        connections: [
          {
            id: "do",
            name: "Prod DigitalOcean",
            cloud: "digitalocean",
            status: "ready",
          },
        ],
      })
    return Response.json({})
  })
  return seen
}
function mount(node: React.ReactNode) {
  return render(<ClientProvider client={forgeClient}>{node}</ClientProvider>)
}

it("lists GPU targets with fit and price", async () => {
  const seen = stub([cluster, size, busyBox])
  const onSelect = vi.fn()
  const user = userEvent.setup()
  mount(<GpuTargets minMemoryGib={24} count={1} onSelect={onSelect} />)

  expect(
    await screen.findByText("All of this server's NVIDIA L4 GPUs are in use.")
  ).toBeInTheDocument()
  expect(screen.getByText("$0.71/hr per node, estimate")).toBeInTheDocument()
  // A size row is named by its slug; the account is looked up by connection.
  expect(await screen.findByText(/Prod DigitalOcean/)).toBeInTheDocument()
  expect(screen.getByText("$3.39/hr")).toBeInTheDocument()
  // An unknown price is never shown as $0.
  const box = screen.getByRole("row", { name: /gpu-box/ })
  expect(within(box).getByText("Price unknown")).toBeInTheDocument()
  expect(within(box).queryByText(/\$0/)).not.toBeInTheDocument()

  expect(
    screen.queryByRole("button", { name: "Choose gpu-box" })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Choose gpu-cluster" }))
  expect(onSelect).toHaveBeenCalledTimes(1)
  expect(onSelect.mock.calls[0]![0]).toMatchObject({
    connectionId: "k8s",
    fits: true,
  })
  expect(seen).toContain("/api/v1/gpu-targets?count=1&min_memory_gib=24")
})

it("names the accounts whose GPU sizes couldn't be read", async () => {
  stub(
    [size],
    [
      {
        connection_id: "other",
        name: "Old DigitalOcean",
        message: "DigitalOcean refused the token.",
      },
    ]
  )
  mount(<GpuTargets />)
  expect(
    await screen.findByText("Old DigitalOcean: DigitalOcean refused the token.")
  ).toBeInTheDocument()
})

it("totals what running GPU servers cost on the Infrastructure page", async () => {
  stub([
    cluster,
    size,
    busyBox,
    {
      ...busyBox,
      connection_id: "h100-box",
      name: "h100-box",
      accelerator: h100,
      fits: true,
      free: 1,
      hourly_usd: 3.39,
      hourly_price_known: true,
    },
  ])
  mount(<InfrastructureGpus />)
  expect(
    await screen.findByText("Running GPU servers cost about $3.39/hr together.")
  ).toBeInTheDocument()
  expect(
    screen.getByText(
      "That leaves out gpu-box, whose price Loomplane doesn't know."
    )
  ).toBeInTheDocument()
  // Only connected servers and clusters show here, not sizes to create.
  expect(await screen.findByText("gpu-cluster")).toBeInTheDocument()
  expect(screen.queryByText("gpu-h100x1-80gb")).not.toBeInTheDocument()
})

it("leaves the GPU section out when nothing connected has GPUs", async () => {
  const seen = stub([size])
  mount(<InfrastructureGpus />)
  await vi.waitFor(() => expect(seen).toContain("/api/v1/gpu-targets"))
  expect(screen.queryByRole("heading", { name: "GPUs" })).toBeNull()
})
