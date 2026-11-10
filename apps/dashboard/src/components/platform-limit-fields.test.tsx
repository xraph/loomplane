import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { PlatformLimitFields } from "@/components/platform-limit-fields"

afterEach(cleanup)

const values = { vcpus: "4", mem: "8", vms: "10", builds: "2" }

it("shows the four limits and reports an edit with the others kept", async () => {
  const onChange = vi.fn()
  render(<PlatformLimitFields values={values} onChange={onChange} />)
  expect(screen.getByLabelText("vCPUs")).toHaveValue("4")
  expect(screen.getByLabelText("Memory (GiB)")).toHaveValue("8")
  expect(screen.getByLabelText("VMs")).toHaveValue("10")
  expect(screen.getByLabelText("Builds at once")).toHaveValue("2")
  await userEvent.type(screen.getByLabelText("VMs"), "5")
  expect(onChange).toHaveBeenLastCalledWith({ ...values, vms: "105" })
})

it("takes its own labels and flags a value that isn't a whole number", () => {
  render(
    <PlatformLimitFields
      values={{ ...values, vcpus: "x" }}
      onChange={() => {}}
      labels={{
        vcpus: "Limit vCPUs",
        mem: "Limit memory (GiB)",
        vms: "Limit VMs",
        builds: "Limit builds at once",
      }}
    />
  )
  expect(screen.getByLabelText("Limit vCPUs")).toHaveAttribute(
    "aria-invalid",
    "true"
  )
  expect(screen.getByText("Enter a whole number")).toBeInTheDocument()
})
