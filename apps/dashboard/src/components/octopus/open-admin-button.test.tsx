import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { OpenAdminButton } from "@/components/octopus/open-admin-button"
import { mount, panelProps, serve } from "@/components/octopus/test-api"
import { catalogKind } from "@/lib/catalog-kinds"

const permissions = vi.hoisted(() => ({
  can: (() => true) as (permission: string) => boolean,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (p: string) => permissions.can(p) }),
}))
const toasts = vi.hoisted(() => ({ error: vi.fn() }))
vi.mock("sonner", () => ({ toast: toasts }))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  toasts.error.mockReset()
  forgeClient.clear()
  permissions.can = () => true
})

/** A window opened by the button, before and after it is pointed. */
function blankWindow() {
  return {
    opener: {} as unknown,
    location: { href: "about:blank" },
    close: vi.fn(),
  }
}

test("Open Octopus admin opens the proxied URL without showing a password", async () => {
  const writes = serve({ adminUrl: "/catalog-admin/tok/admin" })
  const win = blankWindow()
  const open = vi
    .spyOn(window, "open")
    .mockReturnValue(win as unknown as Window)
  mount(<OpenAdminButton {...panelProps()} />)
  await userEvent.click(
    screen.getByRole("button", { name: /Open Octopus admin/ })
  )
  // The tab opens in the click itself, so a popup blocker lets it through.
  expect(open).toHaveBeenCalledTimes(1)
  expect(open).toHaveBeenCalledWith("about:blank", "_blank")
  expect(win.opener).toBeNull()
  await vi.waitFor(() =>
    expect(win.location.href).toBe("/catalog-admin/tok/admin")
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/environments/env_prod/catalog/edge/admin-sessions",
      body: undefined,
    },
  ])
  expect(document.querySelector("input")).toBeNull()
  expect(document.body).not.toHaveTextContent(/password/i)
  expect(win.close).not.toHaveBeenCalled()
})

test("a failed session closes the blank tab and says why", async () => {
  serve({ adminStatus: 409 })
  const win = blankWindow()
  vi.spyOn(window, "open").mockReturnValue(win as unknown as Window)
  mount(<OpenAdminButton {...panelProps()} />)
  await userEvent.click(
    screen.getByRole("button", { name: /Open Octopus admin/ })
  )
  await vi.waitFor(() => expect(win.close).toHaveBeenCalled())
  expect(win.location.href).toBe("about:blank")
  expect(toasts.error).toHaveBeenCalledWith("Octopus isn't running yet.")
})

test("without infrastructure:modify the button is disabled and says why", async () => {
  permissions.can = (p) => p !== "infrastructure:modify"
  serve()
  const open = vi.spyOn(window, "open")
  mount(<OpenAdminButton {...panelProps()} />)
  const button = screen.getByRole("button", { name: /Open Octopus admin/ })
  expect(button).toBeDisabled()
  await userEvent.hover(button.parentElement!)
  expect(
    await screen.findByText(
      "You need permission to change this project's infrastructure."
    )
  ).toBeInTheDocument()
  expect(open).not.toHaveBeenCalled()
})

test("the header names the gateway, environment and target", async () => {
  serve()
  const octopus = catalogKind("octopus")!
  const Subtitle = () => <p>{octopus.subtitle!(panelProps(), "production")}</p>
  mount(<Subtitle />)
  expect(
    await screen.findByText("API gateway · production · edge-1")
  ).toBeVisible()
})

test("the module puts the button in the header", () => {
  const octopus = catalogKind("octopus")!
  expect(octopus.actions).toBe(OpenAdminButton)
  expect(octopus.title).toBe("Octopus")
  expect(octopus.features).toEqual([
    "Routes discovered over FARP",
    "Routes you add by domain and path",
    "Domains from your organization",
  ])
})
