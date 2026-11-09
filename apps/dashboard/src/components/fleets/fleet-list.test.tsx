import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { FleetList } from "./fleet-list"

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
    <a href={to.replace(/\$(\w+)/g, (_, key: string) => params[key])}>
      {children}
    </a>
  ),
}))

it("submits filters together and resets the page cursor", () => {
  const change = vi.fn()
  render(
    <FleetList
      projectID="project"
      rows={[]}
      query={{ after: "cursor", status: "", external_ref: "" }}
      onQuery={change}
    />
  )
  fireEvent.change(
    screen.getByRole("textbox", { name: "Instance reference" }),
    { target: { value: "sample" } }
  )
  fireEvent.change(screen.getByRole("combobox", { name: "Instance state" }), {
    target: { value: "waiting_approval" },
  })
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }))
  expect(change).toHaveBeenCalledWith({
    status: "waiting_approval",
    external_ref: "sample",
    after: "",
  })
})
it("keeps pagination scoped to the current filters", () => {
  const change = vi.fn()
  render(
    <FleetList
      projectID="project"
      rows={[]}
      nextCursor="next"
      query={{ after: "cursor", status: "ready", external_ref: "sample" }}
      onQuery={change}
    />
  )
  fireEvent.click(screen.getByRole("button", { name: "Next page" }))
  expect(change).toHaveBeenCalledWith({
    after: "next",
    status: "ready",
    external_ref: "sample",
  })
  fireEvent.click(screen.getByRole("button", { name: "First page" }))
  expect(change).toHaveBeenLastCalledWith({
    after: "",
    status: "ready",
    external_ref: "sample",
  })
})
it("disables pagination while refreshing and does not claim a fleet total", () => {
  render(
    <FleetList
      projectID="project"
      rows={[]}
      nextCursor="next"
      busy
      query={{ after: "", status: "", external_ref: "" }}
      onQuery={() => {}}
    />
  )
  expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled()
  expect(screen.getByText("0 on this page")).toBeInTheDocument()
  expect(screen.queryByText(/0 total/)).not.toBeInTheDocument()
})
it("keeps an overlong UTF-8 reference editable without sending an invalid query", () => {
  const change = vi.fn()
  render(
    <FleetList
      projectID="project"
      rows={[]}
      query={{ after: "", status: "", external_ref: "" }}
      onQuery={change}
    />
  )
  const input = screen.getByRole("textbox", { name: "Instance reference" })
  fireEvent.change(input, { target: { value: "é".repeat(65) } })
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }))
  expect(change).not.toHaveBeenCalled()
  expect(screen.getByRole("alert")).toHaveTextContent("reference is too long")
  expect(input).toHaveAttribute("aria-invalid", "true")
  fireEvent.change(input, { target: { value: "é".repeat(64) } })
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }))
  expect(change).toHaveBeenCalledWith({
    after: "",
    status: "",
    external_ref: "é".repeat(64),
  })
})
it("preserves correction controls when a page read fails", () => {
  const change = vi.fn()
  render(
    <FleetList
      projectID="project"
      rows={[]}
      query={{
        after: "expired-cursor",
        status: "ready",
        external_ref: "sample",
      }}
      onQuery={change}
      content={<p role="alert">Could not load this page</p>}
    />
  )
  expect(screen.getByRole("alert")).toBeInTheDocument()
  expect(screen.queryByText("0 on this page")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
  expect(change).toHaveBeenCalledWith({
    after: "",
    status: "",
    external_ref: "",
  })
})
