import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { ResourceInventory } from "./inventory"

const resource = {
  id: "r",
  name: "orders",
  provider: "digitalocean",
  connectionId: "c",
  accountIdentity: "Production",
  externalId: "db",
  kind: "postgres",
  region: "nyc3",
  status: "online",
  endpointId: "",
  ownership: "external",
  observedAt: "2026-10-31T12:00:00Z",
  revision: 1,
}
afterEach(cleanup)
const base = {
  resources: [resource],
  connections: [],
  now: new Date("2026-10-31T12:15:00Z").getTime(),
  onSelect: vi.fn(),
  onSync: vi.fn(),
  connection: "",
}
test("shows provider health, credentials and observation separately, and opens actual inventory", async () => {
  render(<ResourceInventory {...base} />)
  const row = screen.getByRole("row", { name: /orders/ })
  expect(within(row).getByText("online")).toBeInTheDocument()
  expect(within(row).getByText("Credentials needed")).toBeInTheDocument()
  expect(within(row).getByText("Observed")).toBeInTheDocument()
  await userEvent.click(within(row).getByRole("button", { name: "orders" }))
  expect(base.onSelect).toHaveBeenCalledWith(resource)
})
test("filtered results use shared empty state with a working reset", async () => {
  render(<ResourceInventory {...base} />)
  await userEvent.type(
    screen.getByRole("textbox", { name: "Search resources" }),
    "absent"
  )
  expect(
    screen.getByText("No matching resources").closest('[data-slot="empty"]')
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Clear filters" }))
  expect(screen.getByRole("button", { name: "orders" })).toBeInTheDocument()
})
test("ownership tabs filter without changing stored readiness", async () => {
  render(<ResourceInventory {...base} />)
  await userEvent.click(screen.getByRole("tab", { name: "Loomplane-created" }))
  expect(screen.getByText("No matching resources")).toBeInTheDocument()
  await userEvent.click(screen.getByRole("tab", { name: "External" }))
  expect(screen.getByText("Credentials needed")).toBeInTheDocument()
})
test("an empty inventory invites discovery through the shared action", async () => {
  render(<ResourceInventory {...base} resources={[]} />)
  expect(screen.getByText("Sync your first account")).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Sync accounts" }))
  expect(base.onSync).toHaveBeenCalled()
})
test("unverified lifecycle ownership remains explicit in inventory", () => {
  render(
    <ResourceInventory
      {...base}
      resources={[{ ...resource, ownershipState: "unverified" }]}
    />
  )
  expect(screen.getByText("Unverified")).toBeInTheDocument()
})
