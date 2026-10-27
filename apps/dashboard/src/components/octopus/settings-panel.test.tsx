import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { SettingsPanel } from "@/components/octopus/settings-panel"
import {
  mockStatus,
  mount,
  panelProps,
  serve,
} from "@/components/octopus/test-api"

const permissions = vi.hoisted(() => ({
  can: (() => true) as (permission: string) => boolean,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (p: string) => permissions.can(p) }),
}))
const toasts = vi.hoisted(() => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }),
}))
vi.mock("sonner", () => ({ toast: toasts.toast }))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  toasts.toast.mockReset()
  toasts.toast.error.mockReset()
  forgeClient.clear()
  permissions.can = () => true
})

const section = (name: string) => screen.getByRole("region", { name })

test("settings shows ports, blocked paths, version and sync", async () => {
  const writes = serve()
  mount(<SettingsPanel {...panelProps()} />)
  const ports = await screen.findByRole("region", { name: "Ports" })
  expect(ports).toHaveTextContent("Gateway 8080, routed through your domains.")
  expect(ports).toHaveTextContent("Admin and FARP 9090, private to Loomplane.")

  const blocked = section("Blocked on your domains")
  for (const path of ["/admin", "/_farp", "/__farp"])
    expect(within(blocked).getByText(path)).toHaveClass("font-mono")

  expect(section("Credentials")).toHaveTextContent(
    "Loomplane generated the admin password, session secret, API token and FARP token and keeps them in Vault. You don't need the password: Open Octopus admin signs you in."
  )
  const version = section("Version")
  expect(version).toHaveTextContent("Octopus 0.5.0")
  expect(version).toHaveTextContent("Started 3 hours ago")

  const sync = section("Sync")
  expect(sync).toHaveTextContent("Last pushed 2 minutes ago")
  expect(section("Removing Octopus")).toHaveTextContent(
    "Remove edge from the manifest and deploy to delete it. Detach its domains first."
  )
  expect(within(section("Removing Octopus")).queryByRole("button")).toBeNull()

  await userEvent.click(
    within(sync).getByRole("button", { name: "Push routes again" })
  )
  await vi.waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/p/environments/env_prod/octopus/edge/sync",
        body: undefined,
      },
    ])
  )
})

test("settings shows the sync error and hides the push without infrastructure:modify", async () => {
  permissions.can = (p) => p !== "infrastructure:modify"
  serve({
    status: {
      ...mockStatus(),
      sync_error: "Octopus refused the route: upstream lp-web-3000 is unknown.",
    },
  })
  mount(<SettingsPanel {...panelProps()} />)
  const sync = await screen.findByRole("region", { name: "Sync" })
  expect(
    within(sync).getByText(
      "Octopus refused the route: upstream lp-web-3000 is unknown."
    )
  ).toBeVisible()
  expect(
    within(sync).queryByRole("button", { name: "Push routes again" })
  ).toBeNull()
})

test("settings never shows a secret value", async () => {
  serve()
  const view = mount(<SettingsPanel {...panelProps()} />)
  expect(
    await screen.findByRole("region", { name: "Credentials" })
  ).toBeVisible()
  // The status has no secret in it, and nothing on the page looks like one.
  expect(JSON.stringify(mockStatus())).not.toMatch(
    /password|secret|token"\s*:/i
  )
  const walker = document.createTreeWalker(view.container, NodeFilter.SHOW_TEXT)
  const texts: string[] = []
  while (walker.nextNode()) texts.push(walker.currentNode.textContent ?? "")
  expect(texts.join(" ")).toContain("Vault")
  for (const text of texts) expect(text).not.toMatch(/[A-Za-z0-9_\-+/=]{32,}/)
  for (const el of view.container.querySelectorAll("*"))
    for (const attr of el.attributes)
      expect(attr.value).not.toMatch(/^[A-Za-z0-9_\-+/=]{32,}$/)
})

test("loading, denied and failed requests stay distinct on each tab", async () => {
  serve({ statusPending: true })
  const view = mount(<SettingsPanel {...panelProps()} />)
  expect(await screen.findByLabelText("Loading settings")).toBeVisible()
  view.unmount()
  forgeClient.clear()

  serve({ statusStatus: 403 })
  const denied = mount(<SettingsPanel {...panelProps()} />)
  expect(
    await screen.findByText("You can't see this gateway's settings")
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  denied.unmount()
  forgeClient.clear()

  serve({ statusStatus: 500 })
  mount(<SettingsPanel {...panelProps()} />)
  expect(await screen.findByRole("button", { name: "Retry" })).toBeVisible()
  expect(screen.queryByText("You can't see this gateway's settings")).toBeNull()
})
