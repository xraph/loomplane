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
import { PlatformCloudTenants } from "@/components/platform-cloud-tenants"

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast: toasts }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  forgeClient.clear()
})

const row = (over: Record<string, unknown> = {}) => ({
  org_id: "o1",
  org_name: "Acme",
  limits: { vcpus: 4, memory_mib: 8192, vms: 10, concurrent_builds: 1 },
  use: { vcpus: 3, memory_mib: 4096, vms: 2, concurrent_builds: 0 },
  tenant: { org_id: "o1", revision: 1, status: "enabled", suspend_reasons: [] },
  ...over,
})

type Call = { method: string; path: string; body: unknown }

function stub(rows: unknown[], calls: Call[] = []) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      const method = init?.method ?? "GET"
      if (method !== "GET") {
        calls.push({
          method,
          path,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        })
        return Response.json(
          { org_id: "o1", status: "enabled", revision: 2, suspend_reasons: [] },
          { status: 202 }
        )
      }
      return Response.json({ tenants: rows })
    })
  )
  return calls
}

function renderTenants() {
  return render(
    <ClientProvider client={forgeClient}>
      <PlatformCloudTenants />
    </ClientProvider>
  )
}

it("shows use against limits", async () => {
  stub([row()])
  renderTenants()
  expect(await screen.findByText("3 of 4 vCPUs")).toBeTruthy()
})

it("warns when a limit drops below use", async () => {
  const calls = stub([row()])
  const user = userEvent.setup()
  renderTenants()
  await user.click(
    await screen.findByRole("button", { name: "Edit limits for Acme" })
  )
  const dialog = await screen.findByRole("dialog")
  const note =
    "Running work keeps running. New work is refused until use is under the limit."
  expect(within(dialog).queryByText(note)).toBeNull()
  const v = within(dialog).getByLabelText("vCPUs")
  await user.clear(v)
  await user.type(v, "2")
  expect(within(dialog).getByText(note)).toBeTruthy()
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  await waitFor(() => expect(calls).toHaveLength(1))
  expect(calls[0].method).toBe("PUT")
  expect(calls[0].body).toMatchObject({
    status: "enabled",
    limits: { vcpus: 2 },
  })
})

it("suspends through the suspend endpoint without a body", async () => {
  const calls = stub([row()])
  const user = userEvent.setup()
  renderTenants()
  await user.click(await screen.findByRole("button", { name: "Suspend Acme" }))
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Suspend" }))
  await waitFor(() => expect(calls).toHaveLength(1))
  expect(calls[0].path).toContain("/tenants/o1/suspend")
  expect(calls[0].body).toBeUndefined()
})

it("shows Restore only for an owner suspension", async () => {
  const calls = stub([
    row({
      tenant: {
        org_id: "o1",
        revision: 1,
        status: "enabled",
        suspend_reasons: ["spending_limit"],
      },
    }),
    row({
      org_id: "o2",
      org_name: "Beta",
      tenant: {
        org_id: "o2",
        revision: 1,
        status: "enabled",
        suspend_reasons: ["platform_owner"],
      },
    }),
  ])
  const user = userEvent.setup()
  renderTenants()
  await screen.findByText("Beta")
  expect(screen.queryByRole("button", { name: "Restore Acme" })).toBeNull()
  await user.click(screen.getByRole("button", { name: "Restore Beta" }))
  await waitFor(() => expect(calls).toHaveLength(1))
  expect(calls[0].path).toContain("/tenants/o2/restore")
})

it("enables and disables through the set route", async () => {
  const calls = stub([row()])
  const user = userEvent.setup()
  renderTenants()
  await user.click(await screen.findByRole("button", { name: "Disable Acme" }))
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Disable" }))
  await waitFor(() => expect(calls).toHaveLength(1))
  expect(calls[0].method).toBe("PUT")
  expect(calls[0].body).toMatchObject({ status: "disabled" })
})

it("sends nothing to disable or suspend until you confirm", async () => {
  const calls = stub([row()])
  const user = userEvent.setup()
  renderTenants()
  for (const [button, action] of [
    ["Disable Acme", "Disable"],
    ["Suspend Acme", "Suspend"],
  ] as const) {
    await user.click(await screen.findByRole("button", { name: button }))
    const dialog = await screen.findByRole("alertdialog")
    expect(within(dialog).getByText(`${action} Acme?`)).toBeTruthy()
    expect(
      within(dialog).getByText(/Acme/, { selector: "p, div" }).textContent
    ).toMatch(/running Loomplane Cloud work/i)
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
  }
  expect(calls).toEqual([])
})

it("says the enable was recorded", async () => {
  stub([
    row({
      tenant: {
        org_id: "o1",
        revision: 1,
        status: "disabled",
        suspend_reasons: [],
      },
    }),
  ])
  const user = userEvent.setup()
  renderTenants()
  await user.click(await screen.findByRole("button", { name: "Enable Acme" }))
  await waitFor(() =>
    expect(toasts.success).toHaveBeenCalledWith(
      "Workspace enabled. Recorded. Running work may take a moment to stop or start."
    )
  )
})

it("names a workspace by its ID when it has no name", async () => {
  stub([row({ org_name: "" })])
  renderTenants()
  expect(await screen.findByText("o1")).toBeTruthy()
  expect(screen.getByRole("button", { name: "Disable o1" })).toBeTruthy()
})

it("tells a disabled workspace it starts again when enabled", async () => {
  stub([
    row({
      tenant: {
        org_id: "o1",
        revision: 1,
        status: "disabled",
        suspend_reasons: ["platform_owner"],
      },
    }),
  ])
  renderTenants()
  expect(
    await screen.findByText(
      "Loomplane Cloud work is stopped by the instance owner. It starts again when they enable it."
    )
  ).toBeTruthy()
  expect(screen.getByText("Disabled")).toBeTruthy()
})

it("never says undefined for a reason it doesn't know", async () => {
  stub([
    row({
      tenant: {
        org_id: "o1",
        revision: 1,
        status: "enabled",
        suspend_reasons: ["quota_review"],
      },
    }),
  ])
  renderTenants()
  expect(
    await screen.findByText(
      "Loomplane Cloud work is stopped because something else needs attention. It starts again when it is resolved."
    )
  ).toBeTruthy()
  expect(document.body.textContent).not.toContain("undefined")
})
