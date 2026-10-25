import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { FabriqTenants } from "@/components/fabriq/fabriq-tenants"
import { mount, panelProps, serve } from "@/components/fabriq/test-api"
import { validTenantId } from "@/lib/fabriq"

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

test("tenant ids are lowercase letters, digits and dashes", () => {
  expect(validTenantId("umbrella")).toBe(true)
  expect(validTenantId("acme-2")).toBe(true)
  expect(validTenantId("2fast")).toBe(true)
  expect(validTenantId("a".repeat(63))).toBe(true)
  for (const bad of [
    "",
    "Umbrella",
    "-acme",
    "acme_corp",
    "acme corp",
    "a".repeat(64),
  ])
    expect(validTenantId(bad)).toBe(false)
})

test("new tenant validates and creates", async () => {
  const writes = serve()
  mount(<FabriqTenants {...panelProps()} />)
  await screen.findByText("acme")
  await userEvent.click(screen.getByRole("button", { name: "New tenant" }))
  const create = screen.getByRole("button", { name: "Create tenant" })
  const field = screen.getByRole("textbox", { name: "Tenant ID" })
  expect(field).toHaveAttribute("placeholder", "umbrella")
  expect(
    screen.getByText(
      "Lowercase letters, digits and dashes. Apps use it in the DSN and in requests."
    )
  ).toBeVisible()
  expect(create).toBeDisabled()
  await userEvent.type(field, "Umbrella")
  expect(create).toBeDisabled()
  await userEvent.clear(field)
  await userEvent.type(field, "umbrella")
  expect(create).toBeEnabled()
  await userEvent.click(create)
  const row = (await screen.findByText("umbrella")).closest("tr")!
  expect(within(row).getByText("Just now")).toBeVisible()
  expect(await within(row).findByText("None")).toBeVisible()
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/environments/env_prod/fabriq/fabriq/tenants",
      body: { tenant_id: "umbrella" },
    },
  ])
  expect(screen.queryByRole("textbox", { name: "Tenant ID" })).toBeNull()
})

test("default for comes from the manifest", async () => {
  serve()
  mount(<FabriqTenants {...panelProps()} />)
  const acme = (await screen.findByText("acme")).closest("tr")!
  expect(await within(acme).findByText("api, billing")).toBeVisible()
  const globex = screen.getByText("globex").closest("tr")!
  expect(await within(globex).findByText("web")).toBeVisible()
  expect(
    screen.getByText(
      "Each tenant's rows are kept apart by row-level security in one database."
    )
  ).toBeVisible()
  expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(
    ["Tenant", "Created", "Default for", "Actions"]
  )
})

test("a tenant nothing defaults to reads None", async () => {
  serve({
    services: {
      api: { needs: ["fabriq"], need_options: { fabriq: { tenant: "acme" } } },
    },
  })
  mount(<FabriqTenants {...panelProps()} />)
  const globex = (await screen.findByText("globex")).closest("tr")!
  expect(await within(globex).findByText("None")).toBeVisible()
})

test("open in admin opens the console in a new tab", async () => {
  serve({ adminUrl: "/catalog-admin/abc/" })
  const open = vi.spyOn(window, "open").mockReturnValue(null)
  mount(<FabriqTenants {...panelProps()} />)
  await screen.findByText("acme")
  await userEvent.click(
    screen.getAllByRole("button", { name: "Open in admin" })[0]
  )
  await vi.waitFor(() =>
    expect(open).toHaveBeenCalledWith(
      "/catalog-admin/abc/",
      "_blank",
      "noopener,noreferrer"
    )
  )
})

test("tenants say when Fabriq isn't reachable", async () => {
  serve({ down: "connection refused" })
  mount(<FabriqTenants {...panelProps()} />)
  expect(
    await screen.findByText(
      "Fabriq isn't reachable right now. Connection refused."
    )
  ).toBeVisible()
  expect(screen.queryByRole("table")).toBeNull()
})

test("viewers can't open the admin from a tenant", async () => {
  permissions.can = (p) => p !== "infrastructure:modify"
  serve()
  mount(<FabriqTenants {...panelProps()} />)
  await screen.findByText("acme")
  expect(screen.queryByRole("button", { name: "Open in admin" })).toBeNull()
})

test("viewers cannot create tenants", async () => {
  permissions.can = (p) => p !== "deployment:create"
  serve()
  mount(<FabriqTenants {...panelProps()} />)
  await screen.findByText("acme")
  expect(screen.queryByRole("button", { name: "New tenant" })).toBeNull()
})

test("no tenants uses the shared empty state with its next action", async () => {
  serve({ tenants: [] })
  mount(<FabriqTenants {...panelProps()} />)
  expect(await screen.findByText("No tenants yet")).toBeVisible()
  expect(screen.queryByRole("table")).toBeNull()
})

test("tenants scrolling is keyboard accessible and keeps table semantics", async () => {
  serve()
  mount(<FabriqTenants {...panelProps()} />)
  const region = await screen.findByRole("region", { name: "Tenant rows" })
  const table = within(region).getByRole("table")
  for (const heading of within(table).getAllByRole("columnheader"))
    expect(heading).toHaveAttribute("scope", "col")
  screen.getByRole("button", { name: "New tenant" }).focus()
  await userEvent.tab()
  expect(region).toHaveFocus()
  await userEvent.tab()
  expect(table).toContainElement(document.activeElement as HTMLElement)
})
