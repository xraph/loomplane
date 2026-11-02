import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test } from "vitest"
import { ResourceSyncDialog } from "./sync-dialog"

afterEach(cleanup)
const account = {
  id: "c",
  name: "Production",
  cloud: "digitalocean",
  status: "ready",
}
test("a denied retry stays visible as failure instead of empty success", async () => {
  render(
    <ResourceSyncDialog
      accounts={[account]}
      syncs={[]}
      open
      onOpenChange={() => {}}
      onSync={async () => {
        throw new Error("Read permission denied")
      }}
    />
  )
  await userEvent.click(screen.getByRole("button", { name: "Sync Production" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Read permission denied"
  )
})
test("sync displays retained partial coverage and does not claim success", async () => {
  render(
    <ResourceSyncDialog
      accounts={[account]}
      syncs={[
        {
          id: "s",
          connectionId: "c",
          status: "partial",
          error: "One scope could not be read",
          startedAt: "2026-10-31T12:00:00Z",
        },
      ]}
      open
      onOpenChange={() => {}}
      onSync={async () => {}}
    />
  )
  expect(screen.getByText("partial")).toBeInTheDocument()
  expect(screen.getByText(/One scope could not be read/)).toBeInTheDocument()
})
test("inventory readers can inspect failures without being offered account mutation", () => {
  render(
    <ResourceSyncDialog
      accounts={[account]}
      syncs={[]}
      open
      canSync={false}
      onOpenChange={() => {}}
      onSync={async () => {}}
    />
  )
  expect(screen.getByRole("button", { name: "Sync Production" })).toBeDisabled()
})
