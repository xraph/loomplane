import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { PlatformCloudHosts } from "@/components/platform-cloud-hosts"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const evidence = (over: Record<string, unknown> = {}) => ({
  isolation: true,
  platform: true,
  smt_active: false,
  denied_ports: [25, 465, 587],
  per_org_vms: 2,
  per_org_vcpus: 2,
  per_org_memory_mib: 2048,
  checked_at: new Date().toISOString(),
  ...over,
})

const host = (over: Record<string, unknown> = {}) => ({
  id: "h1",
  host_id: "host-one",
  endpoint: "h1.example.com:8443",
  region_id: "r1",
  status: "active",
  placements: 0,
  generation: 1,
  revision: 1,
  client_fingerprint: "fp-list",
  allotment: { vcpus: 8, memory_mib: 32768, disk_mib: 204800, vms: 20 },
  use: { vcpus: 1, memory_mib: 1024, disk_mib: 1024, vms: 1 },
  evidence: evidence(),
  ...over,
})

function stub(
  hosts: unknown[],
  onPost?: (path: string) => Response | undefined
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST") {
        const r = onPost?.(path)
        if (r) return r
      }
      if (path.includes("cloud/regions"))
        return Response.json({
          regions: [
            {
              id: "r1",
              name: "eu",
              display_name: "Europe",
              status: "open",
              revision: 1,
            },
          ],
        })
      return Response.json({ hosts })
    })
  )
}

function renderHosts() {
  return render(
    <ClientProvider client={forgeClient}>
      <PlatformCloudHosts />
    </ClientProvider>
  )
}

it("shows each failing check on a host", async () => {
  stub([host({ evidence: evidence({ smt_active: true }) })])
  renderHosts()
  expect(await screen.findByText("SMT is on")).toBeTruthy()
})

it("shows nothing extra for a healthy host", async () => {
  stub([host()])
  renderHosts()
  await screen.findByText("host-one")
  expect(screen.queryByText("SMT is on")).toBeNull()
})

it("shows the client certificate once", async () => {
  const cert = "-----BEGIN CERTIFICATE-----SECRETCERT"
  const fp = "sha256:SECRETFP"
  stub([host()], (path) =>
    path.endsWith("/platform/cloud/hosts")
      ? Response.json({
          client_certificate: cert,
          client_fingerprint: fp,
          host: host({ id: "h2" }),
        })
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  await screen.findByText("host-one")
  await user.click(screen.getByRole("button", { name: "Add host" }))
  const dialog = await screen.findByRole("dialog")
  await within(dialog).findByRole("option", { name: "Europe" })
  await user.type(
    within(dialog).getByLabelText("Endpoint"),
    "h2.example.com:8443"
  )
  await user.type(within(dialog).getByLabelText("Host ID"), "host-two")
  await user.click(within(dialog).getByRole("button", { name: "Add host" }))
  expect(await within(dialog).findByText(fp)).toBeTruthy()
  expect(within(dialog).getByDisplayValue(cert)).toBeTruthy()
  expect(
    within(dialog).getByRole("button", { name: "Copy certificate" })
  ).toBeTruthy()
  expect(
    within(dialog).getByRole("button", { name: "Copy fingerprint" })
  ).toBeTruthy()

  await user.click(within(dialog).getByRole("button", { name: "Done" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(document.body.innerHTML).not.toContain("SECRETCERT")
  expect(document.body.innerHTML).not.toContain("SECRETFP")

  await user.click(screen.getByRole("button", { name: "Add host" }))
  const again = await screen.findByRole("dialog")
  expect(again.innerHTML).not.toContain("SECRETCERT")
  expect(again.innerHTML).not.toContain("SECRETFP")
})

it("asks before retiring", async () => {
  let retired = false
  stub([host()], (path) => {
    if (path.endsWith("/retire")) {
      retired = true
      return new Response(null, { status: 204 })
    }
  })
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Retire host-one" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(within(dialog).getByText("Retire host-one?")).toBeTruthy()
  expect(retired).toBe(false)
  await user.click(within(dialog).getByRole("button", { name: "Retire" }))
  await waitFor(() => expect(retired).toBe(true))
})

it("disables retire while a host has workloads", async () => {
  stub([host({ placements: 2 })])
  renderHosts()
  const btn = (await screen.findByRole("button", {
    name: "Retire host-one",
  })) as HTMLButtonElement
  expect(btn.disabled).toBe(true)
  expect(screen.getByText("Move or remove its 2 workloads first")).toBeTruthy()
})

it("uses the singular for one workload", async () => {
  stub([host({ placements: 1 })])
  renderHosts()
  expect(
    await screen.findByText("Move or remove its 1 workload first")
  ).toBeTruthy()
})
