import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it } from "vitest"
import { MockCloudInfrastructure } from "./cloud-infrastructure"
afterEach(cleanup)
it("keeps pool and DNS zone actions inside the shared empty states", async () => {
  const user = userEvent.setup()
  const { rerender } = render(
    <MockCloudInfrastructure tab="Servers" failure={false} />
  )
  const pool = screen
    .getByText("No server pool yet")
    .closest("[data-slot=empty]")
  expect(pool).toBeInTheDocument()
  await user.click(
    within(pool as HTMLElement).getByRole("button", { name: "Add pool" })
  )
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  expect(screen.queryByText("No server pool yet")).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Edit pool budgets" })
  ).toBeInTheDocument()
  rerender(<MockCloudInfrastructure tab="Domains" failure={false} />)
  const zone = screen.getByText("No DNS zone yet").closest("[data-slot=empty]")
  expect(zone).toBeInTheDocument()
  await user.click(
    within(zone as HTMLElement).getByRole("button", { name: "Save DNS zone" })
  )
  expect(screen.queryByText("No DNS zone yet")).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Verify zone" })
  ).toBeInTheDocument()
})
it("keeps a stale server review from applying and records accepted enrollment separately", async () => {
  const user = userEvent.setup()
  const { rerender } = render(
    <MockCloudInfrastructure tab="Servers" failure={false} />
  )
  await user.click(screen.getByRole("button", { name: "Create server" }))
  await user.type(
    screen.getByRole("textbox", { name: "Server name" }),
    "new-host"
  )
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Simulate stale review" })
  )
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  expect(screen.getByRole("alert")).toHaveTextContent(/Review the server again/)
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  expect(screen.getByText(/new-host · waiting/)).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Verify enrollment" }))
  expect(screen.getByText(/new-host · ready/)).toBeInTheDocument()
  rerender(<MockCloudInfrastructure tab="Activity" failure={false} />)
  expect(
    screen.getByText(/new-host: enrollment and runtime health verified/)
  ).toBeInTheDocument()
})
it("requires zone ownership before route review, then preserves separate TLS failure and retry", async () => {
  const user = userEvent.setup()
  const { rerender } = render(<MockCloudInfrastructure tab="Domains" failure />)
  await user.click(screen.getByRole("button", { name: "Save DNS zone" }))
  await user.click(screen.getByRole("button", { name: "Verify zone" }))
  await user.click(screen.getByRole("button", { name: "Add route" }))
  await user.click(screen.getByRole("button", { name: "Review route" }))
  await user.click(screen.getByRole("button", { name: "Apply reviewed route" }))
  expect(
    screen.getByText(/DNS pending · TLS pending · Backend pending/)
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Verify route" }))
  expect(
    screen.getByText(/DNS verified · TLS pending · Backend pending/)
  ).toBeInTheDocument()
  await user.click(
    screen.getByRole("button", { name: "Retry route operation" })
  )
  expect(
    screen.getByText(/DNS verified · TLS verified · Backend verified/)
  ).toBeInTheDocument()
  rerender(<MockCloudInfrastructure tab="Activity" failure />)
  expect(screen.getByText(/DNS, TLS and backend verified/)).toBeInTheDocument()
})

it("rejects invalid external TTL values and only updates ready external routes", async () => {
  const user = userEvent.setup()
  render(<MockCloudInfrastructure tab="Domains" failure={false} />)
  await user.click(screen.getByRole("button", { name: "Save DNS zone" }))
  await user.click(screen.getByRole("button", { name: "Verify zone" }))
  await user.click(screen.getByRole("button", { name: "Add route" }))
  const ttl = screen.getByRole("spinbutton", {
    name: "Maximum previously served TTL (seconds)",
  })
  for (const value of ["1", "59", "86401", "60.5"]) {
    await user.clear(ttl)
    await user.type(ttl, value)
    expect(screen.getByRole("button", { name: "Review route" })).toBeDisabled()
  }
  await user.clear(ttl)
  await user.type(ttl, "60")
  await user.click(screen.getByRole("button", { name: "Review route" }))
  await user.click(screen.getByRole("button", { name: "Apply reviewed route" }))
  expect(
    screen.queryByRole("button", { name: "Update external TTL" })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Verify route" }))
  await user.click(screen.getByRole("button", { name: "Update external TTL" }))
  const bound = screen.getByRole("spinbutton", {
    name: "Maximum previously served TTL (seconds)",
  })
  await user.clear(bound)
  await user.type(bound, "86401")
  expect(screen.getByRole("button", { name: "Save TTL bound" })).toBeDisabled()
})
it("does not offer a move outside a pool containing only the draining source", async () => {
  const user = userEvent.setup()
  render(<MockCloudInfrastructure tab="Servers" failure={false} />)
  await user.click(screen.getByRole("button", { name: "Add pool" }))
  await user.click(screen.getByRole("checkbox", { name: "shared-us-2" }))
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  await user.click(
    screen.getAllByRole("button", { name: "Inspect / drain" })[0]!
  )
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  expect(
    screen.getByRole("button", { name: "Preview service move" })
  ).toBeDisabled()
  expect(screen.getByText(/No eligible destination/)).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Verify moved release and routes" })
  ).not.toBeInTheDocument()
})
it("invalidates a move when its reviewed destination leaves the pool and does not clear source workloads", async () => {
  const user = userEvent.setup()
  render(<MockCloudInfrastructure tab="Servers" failure={false} />)
  await user.click(screen.getByRole("button", { name: "Add pool" }))
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  await user.click(
    screen.getAllByRole("button", { name: "Inspect / drain" })[0]!
  )
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  await user.click(screen.getByRole("button", { name: "Preview service move" }))
  await user.click(
    screen.getByRole("button", { name: "Accept reviewed service move" })
  )
  expect(screen.getByText(/15 minutes after acceptance/)).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Edit pool budgets" }))
  await user.click(screen.getByRole("checkbox", { name: "shared-us-2" }))
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  expect(
    screen.queryByRole("button", { name: "Create deployment plan" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", {
      name: "Simulate reviewed deployment completion",
    })
  ).not.toBeInTheDocument()
  expect(screen.getByText(/2 workloads/)).toBeInTheDocument()
})

it("moves only the reviewed API service to the selected host and preserves the unrelated Jobs workload", async () => {
  const user = userEvent.setup()
  render(<MockCloudInfrastructure tab="Servers" failure={false} />)
  await user.click(screen.getByRole("button", { name: "Create server" }))
  await user.type(
    screen.getByRole("textbox", { name: "Server name" }),
    "third-host"
  )
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  await user.click(screen.getByRole("button", { name: "Verify enrollment" }))
  await user.click(screen.getByRole("button", { name: "Add pool" }))
  await user.click(screen.getByRole("checkbox", { name: "third-host" }))
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  await user.click(
    screen.getAllByRole("button", { name: "Inspect / drain" })[0]!
  )
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  const target = screen.getByRole("combobox", {
    name: "Move destination server",
  })
  await user.click(target)
  await user.click(await screen.findByRole("option", { name: "third-host" }))
  await user.click(screen.getByRole("button", { name: "Preview service move" }))
  await user.click(
    screen.getByRole("button", { name: "Accept reviewed service move" })
  )
  expect(
    screen.getByText("shared-us-1 · ready · draining").parentElement
  ).toHaveTextContent("2 workloads")
  await user.click(
    screen.getByRole("button", { name: "Create deployment plan" })
  )
  expect(
    screen.getByText("shared-us-1 · ready · draining").parentElement
  ).toHaveTextContent("2 workloads")
  await user.click(
    screen.getByRole("button", {
      name: "Simulate reviewed deployment completion",
    })
  )
  expect(
    screen.getByText("third-host · ready").parentElement
  ).toHaveTextContent("1 workloads")
  const source = screen
    .getByText("shared-us-1 · ready · draining")
    .closest("[role=listitem]")
  const destination = screen
    .getByText("third-host · ready")
    .closest("[role=listitem]")
  expect(source).toHaveTextContent("1 workloads")
  expect(source).toHaveTextContent("Jobs / worker")
  expect(source).not.toHaveTextContent("Web app / api")
  expect(destination).toHaveTextContent("Web app / api")
  expect(destination).not.toHaveTextContent("Jobs / worker")
  expect(screen.getAllByText("Web app / api", { exact: true })).toHaveLength(1)
  expect(screen.getAllByText("Jobs / worker", { exact: true })).toHaveLength(1)
  expect(
    screen.queryByRole("button", {
      name: "Simulate reviewed deployment completion",
    })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Preview reservation recovery" })
  ).toBeDisabled()
  expect(
    screen.getByText("shared-us-2 · ready").parentElement
  ).toHaveTextContent("0 workloads")
})
it("invalidates staged movement when its destination host is disconnected", async () => {
  const user = userEvent.setup()
  render(<MockCloudInfrastructure tab="Servers" failure={false} />)
  await user.click(screen.getByRole("button", { name: "Add pool" }))
  await user.click(screen.getByRole("button", { name: "Save pool budgets" }))
  await user.click(
    screen.getAllByRole("button", { name: "Inspect / drain" })[0]!
  )
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  await user.click(screen.getByRole("button", { name: "Preview service move" }))
  await user.click(
    screen.getByRole("button", { name: "Accept reviewed service move" })
  )
  await user.click(screen.getByRole("button", { name: "Review disconnect" }))
  await user.click(screen.getByRole("button", { name: "Review server" }))
  await user.click(
    screen.getByRole("button", { name: "Apply reviewed server" })
  )
  expect(
    screen.queryByRole("button", { name: "Create deployment plan" })
  ).not.toBeInTheDocument()
  expect(
    screen.getByText("shared-us-1 · ready · draining").parentElement
  ).toHaveTextContent("2 workloads")
})

it("uses shared controls with keyboard selection and clickable checkbox labels", async () => {
  const user = userEvent.setup()
  render(<MockCloudInfrastructure tab="Servers" failure={false} />)
  expect(screen.getAllByRole("list")[0]).toHaveAttribute(
    "data-slot",
    "item-group"
  )
  await user.click(screen.getByRole("button", { name: "Create server" }))
  const size = screen.getByRole("combobox", { name: "Server size" })
  expect(size).toHaveAttribute("data-slot", "select-trigger")
  size.focus()
  await user.keyboard("{ArrowDown}")
  await screen.findByRole("option", { name: "4 CPU / 8 GiB" })
  await user.keyboard("{End}{Enter}")
  expect(size).toHaveTextContent("4 CPU / 8 GiB")
  await user.click(screen.getByRole("button", { name: "Close" }))
  await user.click(
    screen.getByRole("button", { name: "Connect existing server" })
  )
  const trust = screen.getByRole("checkbox", { name: /Confirm pinned SSH/ })
  expect(trust).toHaveAttribute("data-slot", "checkbox")
  expect(trust.closest("[data-slot=field]")).toBeInTheDocument()
  await user.click(
    screen.getByText("Confirm pinned SSH access and Docker installation")
  )
  expect(trust).toBeChecked()
})
