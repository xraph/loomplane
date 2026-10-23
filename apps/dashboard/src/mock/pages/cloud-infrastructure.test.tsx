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
