import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"
import { InstanceOverview } from "./instance-overview"
const instance = {
  id: "instance",
  externalRef: "sample",
  status: "waiting_approval",
  environmentId: "environment",
  profileId: "small",
  stageId: "stage",
  revision: 2,
  desiredGeneration: 2,
  observedGeneration: 1,
  desiredReleaseId: "new",
  observedReleaseId: "old",
  updatedAt: "2026-11-01T12:00:00Z",
}
it("shows an unapplied target without claiming the observed release changed", () => {
  render(<InstanceOverview instance={instance} />)
  expect(screen.getByLabelText("Desired release")).toHaveTextContent("new")
  expect(screen.getByLabelText("Observed release")).toHaveTextContent("old")
  expect(
    screen.getByLabelText("Desired / observed generation")
  ).toHaveTextContent("2 / 1")
  expect(screen.getByText("Waiting approval")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: /approve|retry|scale/i })
  ).not.toBeInTheDocument()
})
it("marks an unobserved release unknown", () => {
  render(
    <InstanceOverview
      instance={{ ...instance, observedReleaseId: "", observedGeneration: 0 }}
    />
  )
  expect(screen.getByLabelText("Observed release")).toHaveTextContent("Unknown")
  expect(
    screen.getByLabelText("Desired / observed generation")
  ).toHaveTextContent("2 / Unknown")
})
