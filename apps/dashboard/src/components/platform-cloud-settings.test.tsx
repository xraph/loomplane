import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { PlatformCloudSettings } from "@/components/platform-cloud-settings"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const view = {
  apex: "apps.example.com",
  attach: "manual",
  default_limits: { vcpus: 4, memory_mib: 8192, vms: 10, concurrent_builds: 2 },
  dns: { provider: "cloudflare", zone: "example.com" },
  dns_credential_saved: true,
  registry: {
    endpoint: "r.example.com",
    auth_endpoint: "",
    repository: "loom",
  },
  registry_credential_saved: true,
}

function stub(answer: Record<string, unknown> = view) {
  const puts: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        puts.push(JSON.parse(String(init.body)) as Record<string, unknown>)
      }
      return Response.json(answer)
    })
  )
  return puts
}

function renderSettings() {
  return render(
    <ClientProvider client={forgeClient}>
      <PlatformCloudSettings />
    </ClientProvider>
  )
}

const credentialKeys = [
  "dns_token",
  "dns_account_id",
  "dns_team_id",
  "registry_username",
  "registry_password",
]

it("never prefills credentials and sends none unless typed", async () => {
  const puts = stub()
  const user = userEvent.setup()
  renderSettings()
  const token = (await screen.findByLabelText("DNS token")) as HTMLInputElement
  expect(token.value).toBe("")
  expect(
    (screen.getByLabelText("Registry password") as HTMLInputElement).value
  ).toBe("")
  expect(screen.getByText(/A DNS credential is saved/)).toBeTruthy()

  await user.click(screen.getByRole("button", { name: "Save settings" }))
  await vi.waitFor(() => expect(puts).toHaveLength(1))
  for (const k of credentialKeys) expect(puts[0]).not.toHaveProperty(k)

  await user.type(screen.getByLabelText("Registry password"), "pw-1")
  await user.click(screen.getByRole("button", { name: "Save settings" }))
  await vi.waitFor(() => expect(puts).toHaveLength(2))
  expect(puts[1].registry_password).toBe("pw-1")
  for (const k of credentialKeys.filter((k) => k !== "registry_password"))
    expect(puts[1]).not.toHaveProperty(k)
})

it("names its controls as they read and picks a DNS provider from a list", async () => {
  const puts = stub()
  const user = userEvent.setup()
  renderSettings()
  expect(
    await screen.findByRole("combobox", { name: "Which workspaces can use it" })
  ).toBeTruthy()
  await user.click(screen.getByRole("combobox", { name: "DNS provider" }))
  const options = (await screen.findAllByRole("option")).map(
    (o) => o.textContent
  )
  expect(options).toEqual(["Cloudflare", "DigitalOcean", "Vercel"])
  await user.click(screen.getByRole("option", { name: "Vercel" }))
  await user.click(screen.getByRole("button", { name: "Save settings" }))
  await vi.waitFor(() => expect(puts).toHaveLength(1))
  expect(puts[0].dns).toMatchObject({ provider: "vercel" })
})

it("keeps untouched memory exact and blocks a blank limit", async () => {
  const puts = stub({
    ...view,
    default_limits: { ...view.default_limits, memory_mib: 1536 },
  })
  const user = userEvent.setup()
  renderSettings()
  const vcpus = await screen.findByLabelText("Limit vCPUs")
  await user.clear(vcpus)
  expect(screen.getByText("Enter a whole number")).toBeTruthy()
  const save = screen.getByRole("button", {
    name: "Save settings",
  }) as HTMLButtonElement
  expect(save.disabled).toBe(true)
  await user.type(vcpus, "6")
  await user.click(save)
  await vi.waitFor(() => expect(puts).toHaveLength(1))
  expect(puts[0].default_limits).toMatchObject({ vcpus: 6, memory_mib: 1536 })
})
