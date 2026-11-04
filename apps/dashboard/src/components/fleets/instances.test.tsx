import { fireEvent, render, screen, within } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { InstanceTable } from "./instances"

vi.mock("@/lib/router", () => ({
  Link: ({
    to,
    params,
    children,
  }: {
    to: string
    params: Record<string, string>
    children: React.ReactNode
  }) => (
    <a
      href={to.replace(/\$(\w+)/g, (_, key: string) =>
        encodeURIComponent(params[key])
      )}
    >
      {children}
    </a>
  ),
}))

const row = {
  id: "instance",
  externalRef: "sample-01",
  status: "waiting_approval",
  profileID: "small",
  desiredReleaseID: "release",
  observedReleaseID: "",
  updatedAt: "2026-11-01T12:00:00Z",
}

it("preserves project scope and does not turn missing observations into zero", () => {
  render(<InstanceTable projectID="project" rows={[row]} />)
  const instance = screen.getByRole("link", { name: "sample-01" })
  expect(instance).toHaveAttribute(
    "href",
    "/projects/project/fleet/instances/instance"
  )
  const cells = within(instance.closest("tr")!).getAllByRole("cell")
  expect(cells.map((cell) => cell.textContent)).toContain("Unknown")
  expect(screen.queryByText("0 / 0 / 0")).not.toBeInTheDocument()
  expect(screen.getByText("Waiting approval")).toBeInTheDocument()
})

it("keeps observed and desired releases distinct", () => {
  render(
    <InstanceTable
      projectID="project"
      rows={[{ ...row, observedReleaseID: "older" }]}
    />
  )
  expect(screen.getByText("older")).toBeInTheDocument()
  expect(screen.getByText("Target: release")).toBeInTheDocument()
})

it("distinguishes empty results from an empty fleet and keeps the action in ZeroState", () => {
  const clear = vi.fn()
  const view = render(
    <InstanceTable projectID="project" rows={[]} filtered onClear={clear} />
  )
  expect(screen.getByText("No matching instances")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
  expect(clear).toHaveBeenCalledOnce()
  expect(
    screen
      .getByRole("button", { name: "Clear filters" })
      .closest('[data-slot="empty"]')
  ).not.toBeNull()
  view.rerender(<InstanceTable projectID="project" rows={[]} />)
  expect(screen.getByText("No instances yet")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: /tour/i })
  ).not.toBeInTheDocument()
})
