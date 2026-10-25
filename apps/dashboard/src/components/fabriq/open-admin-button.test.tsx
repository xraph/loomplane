import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { OpenAdminButton } from "@/components/fabriq/open-admin-button"
import { mount, panelProps, serve } from "@/components/fabriq/test-api"
import { catalogKind } from "@/lib/catalog-kinds"

const permissions = vi.hoisted(() => ({
  can: (() => true) as (permission: string) => boolean,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: (p: string) => permissions.can(p) }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  forgeClient.clear()
  permissions.can = () => true
})

test("open fabriq admin opens the session url", async () => {
  const writes = serve({ adminUrl: "/catalog-admin/abc/" })
  const open = vi.spyOn(window, "open").mockReturnValue(null)
  mount(<OpenAdminButton {...panelProps()} />)
  await userEvent.click(
    screen.getByRole("button", { name: /Open Fabriq admin/ })
  )
  await vi.waitFor(() =>
    expect(open).toHaveBeenCalledWith(
      "/catalog-admin/abc/",
      "_blank",
      "noopener,noreferrer"
    )
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/environments/env_prod/catalog/fabriq/admin-sessions",
      body: undefined,
    },
  ])
})

test("viewers see neither rotate, revoke nor open admin", () => {
  permissions.can = () => false
  serve()
  mount(<OpenAdminButton {...panelProps()} />)
  expect(screen.queryByRole("button", { name: /Open Fabriq admin/ })).toBeNull()
})

test("the module adds the tab, the button and the subtitle", async () => {
  const fabriq = catalogKind("fabriq")!
  expect(fabriq.panels.map((p) => [p.id, p.title, p.order])).toEqual([
    ["keys", "Tenants and keys", 1],
    ["engines", "Engines", 2],
    ["recovery", "Recovery", 3],
    ["settings", "Settings", 4],
  ])
  expect(fabriq.actions).toBe(OpenAdminButton)
  serve()
  const Subtitle = () => <p>{fabriq.subtitle!(panelProps(), "production")}</p>
  mount(<Subtitle />)
  expect(
    await screen.findByText("Hosted Fabriq server · production · edge-1")
  ).toBeVisible()
})
