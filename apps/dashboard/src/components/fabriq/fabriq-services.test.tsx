import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { FabriqServices } from "@/components/fabriq/fabriq-services"
import {
  apiKey,
  mount,
  panelProps,
  serve,
  type Write,
} from "@/components/fabriq/test-api"

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

const minutesAgo = (n: number) =>
  new Date(Date.now() - n * 60_000).toISOString()

const billing = {
  service: "billing",
  tenant: "acme",
  prefix: "fq_19b0",
  state: "active",
  last_used_at: minutesAgo(60),
  last_used_known: true,
}
const jobs = {
  service: "jobs",
  tenant: "globex",
  prefix: "fq_c4d1",
  state: "active",
  last_used_known: true,
}

const rowOf = (service: string) => screen.getByText(service).closest("tr")!
const edits = (writes: Write[]) =>
  writes.filter((w) => w.path.endsWith("/manifest/edits"))

test("services list their key, tenant and last use", async () => {
  serve({ keys: [apiKey, billing, jobs] })
  mount(<FabriqServices {...panelProps()} />)
  await screen.findByText("billing")
  expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(
    ["Service", "Key", "Tenant", "Last used", "Actions"]
  )
  const api = rowOf("api")
  expect(within(api).getByText("production")).toBeVisible()
  expect(within(api).getByText("fq_7c2e…")).toBeVisible()
  expect(within(api).getByText("Active")).toBeVisible()
  expect(within(api).getByText("acme")).toBeVisible()
  expect(within(api).getByText("2 minutes ago")).toBeVisible()
  expect(within(rowOf("billing")).getByText("1 hour ago")).toBeVisible()
  expect(within(rowOf("jobs")).getByText("Never")).toBeVisible()
  expect(
    screen.getByText(/Each service gets its own key in/)
  ).toHaveTextContent(
    "Each service gets its own key in FABRIQ_URL, so revoking one leaves the others working."
  )
  expect(
    screen.getByText(
      "Loomplane keeps its own operator key to manage tenants and keys. It is never shown or given to a service."
    )
  ).toBeVisible()
})

test("last used says when Fabriq doesn't report it", async () => {
  serve({
    keys: [{ ...jobs, last_used_known: false }],
  })
  mount(<FabriqServices {...panelProps()} />)
  expect(await screen.findByText("Not reported")).toBeVisible()
})

test("a linked service without a key yet is issued one on its next release", async () => {
  serve({
    keys: [
      {
        service: "web",
        tenant: "globex",
        state: "unreleased",
        last_used_known: true,
      },
    ],
  })
  mount(<FabriqServices {...panelProps()} />)
  const row = (await screen.findByText("web")).closest("tr")!
  expect(within(row).getByText("Issued on its next release")).toBeVisible()
  expect(within(row).queryByRole("button")).toBeNull()
})

test("link a service edits the manifest without planning", async () => {
  const writes = serve({ keys: [apiKey] })
  mount(<FabriqServices {...panelProps()} />)
  await screen.findByText("api")
  await userEvent.click(screen.getByRole("button", { name: "Link a service" }))
  expect(
    screen.getByText(
      "Takes effect on the service's next release. The app can still pick another tenant per request."
    )
  ).toBeVisible()
  const link = screen.getByRole("button", { name: "Link service" })
  await userEvent.click(screen.getByRole("combobox", { name: "Service" }))
  // billing and web already use Fabriq, api has a key: only jobs is left.
  expect(screen.queryByRole("option", { name: "billing" })).toBeNull()
  await userEvent.click(await screen.findByRole("option", { name: "jobs" }))
  await userEvent.click(
    screen.getByRole("combobox", { name: "Default tenant" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "globex" }))
  await userEvent.click(link)
  await vi.waitFor(() => expect(edits(writes)).toHaveLength(1))
  expect(edits(writes)[0].body).toEqual({
    link: { service: "jobs", need: "fabriq", options: { tenant: "globex" } },
  })
  expect(writes.some((w) => w.path.endsWith("/plans"))).toBe(false)
  await vi.waitFor(() =>
    expect(screen.queryByRole("button", { name: "Link service" })).toBeNull()
  )
})

test("rotate and revoke", async () => {
  const writes = serve({ keys: [apiKey, billing] })
  mount(<FabriqServices {...panelProps()} />)
  await screen.findByText("api")
  await userEvent.click(
    within(rowOf("api")).getByRole("button", { name: "Rotate" })
  )
  await vi.waitFor(() =>
    expect(writes.at(-1)?.path).toBe(
      "/api/v1/projects/p/environments/env_prod/fabriq/fabriq/keys/api/rotate"
    )
  )
  const rotating = await within(rowOf("api")).findByText("Rotating")
  expect(rotating).toHaveAttribute(
    "title",
    "The new key takes effect on the service's next release."
  )
  expect(
    within(rowOf("api")).queryByRole("button", { name: "Rotate" })
  ).toBeNull()

  await userEvent.click(
    within(rowOf("billing")).getByRole("button", { name: "Revoke" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(within(dialog).getByText("Revoke billing's key?")).toBeVisible()
  expect(
    within(dialog).getByText(
      "billing loses access to Fabriq now. Its next release gets a new key unless you remove fabriq from its needs."
    )
  ).toBeVisible()
  await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
  expect(writes.filter((w) => w.path.endsWith("/revoke"))).toEqual([])

  await userEvent.click(
    within(rowOf("billing")).getByRole("button", { name: "Revoke" })
  )
  await userEvent.click(
    await screen.findByRole("button", { name: "Revoke key" })
  )
  const row = rowOf("billing")
  expect(await within(row).findByText("Revoked just now")).toBeVisible()
  expect(within(row).getByText("Revoked")).toBeVisible()
  expect(within(row).queryByRole("button")).toBeNull()
  expect(writes.at(-1)).toMatchObject({
    method: "POST",
    path: "/api/v1/projects/p/environments/env_prod/fabriq/fabriq/keys/billing/revoke",
  })
})

test("viewers can't rotate or revoke", async () => {
  permissions.can = (p) => p !== "secret:manage"
  serve({ keys: [apiKey] })
  mount(<FabriqServices {...panelProps()} />)
  await screen.findByText("api")
  expect(screen.queryByRole("button", { name: "Rotate" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Revoke" })).toBeNull()
})

test("services say when Fabriq isn't reachable", async () => {
  serve({ down: "connection refused" })
  mount(<FabriqServices {...panelProps()} />)
  expect(
    await screen.findByText(
      "Fabriq isn't reachable right now. Connection refused."
    )
  ).toBeVisible()
  expect(screen.queryByRole("table")).toBeNull()
})

test("services scrolling is keyboard accessible and keeps table semantics", async () => {
  serve()
  mount(<FabriqServices {...panelProps()} />)
  const region = await screen.findByRole("region", { name: "Service keys" })
  const table = within(region).getByRole("table")
  for (const heading of within(table).getAllByRole("columnheader"))
    expect(heading).toHaveAttribute("scope", "col")
  screen.getByRole("button", { name: "Link a service" }).focus()
  await userEvent.tab()
  expect(region).toHaveFocus()
  await userEvent.tab()
  expect(table).toContainElement(document.activeElement as HTMLElement)
})
