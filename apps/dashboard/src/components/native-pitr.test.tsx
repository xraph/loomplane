import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { NativePITR } from "@/components/native-pitr"
import type { NativeSource } from "@/client/forge/loomplane/types"

const rights = vi.hoisted(() => ({ manage: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => rights.manage }),
}))
vi.mock("@/components/native-copy-review", () => ({
  NativeCopyReview: ({ restoreTime }: { restoreTime?: string }) => (
    <div role="dialog">Reviewed time {restoreTime}</div>
  ),
}))
afterEach(() => {
  cleanup()
  forgeClient.clear()
  rights.manage = true
  vi.unstubAllGlobals()
})

const source = { id: "source", name: "orders", provider: "rds" } as NativeSource
function show(body: unknown, status = 200, current = source) {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      })
  )
  render(
    <ClientProvider client={forgeClient}>
      <NativePITR source={current} onAccepted={() => {}} />
    </ClientProvider>
  )
}

it("requires an explicit covered UTC second and passes it unchanged to review", async () => {
  const user = userEvent.setup()
  show({
    source_id: "source",
    provider: "rds",
    available: true,
    point_id: "window",
    earliest: "2026-10-21T00:00:00Z",
    latest: "2026-10-22T00:00:00Z",
    observed_at: "2026-10-23T00:00:00Z",
  })
  await user.click(screen.getByRole("button", { name: "Read PITR window" }))
  const input = await screen.findByLabelText("Restore time in UTC")
  const review = screen.getByRole("button", { name: "Review PITR copy" })
  expect(review).toBeDisabled()
  for (const value of [
    "2026-10-22T00:00:00Z",
    "2026-10-20T23:59:59Z",
    "2026-10-21T12:00:00-05:00",
    "2026-10-21T12:00:00.001Z",
  ]) {
    await user.clear(input)
    await user.type(input, value)
    expect(review).toBeDisabled()
  }
  await user.clear(input)
  await user.type(input, "2026-10-21T12:00:03Z")
  await user.click(review)
  expect(await screen.findByRole("dialog")).toHaveTextContent(
    "2026-10-21T12:00:03Z"
  )
})

it("preserves Cloud SQL nanoseconds and compares both window edges exactly", async () => {
  const user = userEvent.setup()
  show(
    {
      source_id: "source",
      provider: "cloud-sql",
      available: true,
      point_id: "window",
      earliest: "2026-10-21T12:00:03.123456787Z",
      latest: "2026-10-21T12:00:03.123456789Z",
    },
    200,
    { ...source, provider: "cloud-sql" }
  )
  await user.click(screen.getByRole("button", { name: "Read PITR window" }))
  const input = await screen.findByLabelText("Restore time in UTC")
  const review = screen.getByRole("button", { name: "Review PITR copy" })
  for (const value of [
    "2026-10-21T12:00:03.123456786Z",
    "2026-10-21T12:00:03.123456789Z",
    "2026-10-21T12:00:03.1234567881Z",
    "2026-02-30T12:00:03Z",
  ]) {
    await user.clear(input)
    await user.type(input, value)
    expect(review).toBeDisabled()
  }
  await user.clear(input)
  await user.type(input, "2026-10-21T12:00:03.123456787Z")
  expect(review).toBeEnabled()
  await user.clear(input)
  await user.type(input, "2026-10-21T12:00:03.123456788Z")
  await user.click(review)
  expect(await screen.findByRole("dialog")).toHaveTextContent(
    "2026-10-21T12:00:03.123456788Z"
  )
})

it("shows unavailable separately from an error and never offers a restore", async () => {
  const user = userEvent.setup()
  show({
    source_id: "source",
    provider: "cloud-sql",
    available: false,
    unavailable_reason: "This recovery profile has no isolated PITR copy path.",
  })
  await user.click(screen.getByRole("button", { name: "Read PITR window" }))
  expect(
    await screen.findByText(
      "This recovery profile has no isolated PITR copy path."
    )
  ).toBeVisible()
  expect(screen.queryByLabelText("Restore time in UTC")).not.toBeInTheDocument()
})

it("keeps read failures visible with a retry and blocks creation for readers", async () => {
  rights.manage = false
  const user = userEvent.setup()
  show({ message: "Provider permission denied" }, 503)
  await user.click(screen.getByRole("button", { name: "Read PITR window" }))
  expect(await screen.findByRole("button", { name: "Retry" })).toBeVisible()
  expect(
    screen.queryByRole("button", { name: "Review PITR copy" })
  ).not.toBeInTheDocument()
})
