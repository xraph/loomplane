import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it } from "vitest"
import { MockCloudInfrastructure } from "./cloud-infrastructure"
afterEach(cleanup)
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

it("pins the selected eligible destination and keeps staging separate from deployment completion", async () => {
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
  const option = screen.getByRole("option", {
    name: "third-host",
  }) as HTMLOptionElement
  await user.selectOptions(target, option.value)
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
  ).toHaveTextContent("2 workloads")
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
