import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"

import { VerificationBadge } from "@/components/ai/models/verification-badge"

it("names each verification in a status tone", () => {
  const { rerender } = render(
    <VerificationBadge status="tested" gpuTitle="NVIDIA L40S" />
  )
  const tested = screen.getByText("Tested on NVIDIA L40S")
  expect(tested.closest("span[class*='text-overdye-ink']")).not.toBeNull()

  rerender(<VerificationBadge status="estimated" />)
  expect(screen.getByText("Estimated")).toBeInTheDocument()
  expect(screen.getByRole("button")).toHaveAccessibleName(
    "Estimated. Memory math only. Loomplane hasn't run this model yet."
  )

  rerender(<VerificationBadge status="untested" />)
  expect(screen.getByText("Untested")).toBeInTheDocument()
  // Never the accent: status colors only.
  expect(
    screen.getByText("Untested").closest("span[class*='text-weld-ink']")
  ).not.toBeNull()
  expect(document.querySelector("[class*='primary']")).toBeNull()
})
