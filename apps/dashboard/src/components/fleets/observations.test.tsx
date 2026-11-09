import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"
import { Observations } from "./observations"

it("shows unavailable replica measurements without zero or healthy claims", () => {
  render(<Observations />)
  expect(
    screen.getByLabelText("Desired / current / ready replicas")
  ).toHaveTextContent("Unknown / Unknown / Unknown")
  expect(screen.getByText("Observation unavailable")).toBeInTheDocument()
  expect(screen.queryByText("Healthy")).not.toBeInTheDocument()
})
it("preserves measured zero and marks partial stale data", () => {
  render(
    <Observations
      summary={{
        desiredReplicas: 2,
        currentReplicas: 1,
        readyReplicas: 0,
        cpuUtilization: 0,
        partial: true,
        freshness: "stale",
        oldestObservation: "2026-11-01T12:00:00Z",
        coverage: { expected: 2, observed: 1, fresh: 0 },
      }}
    />
  )
  expect(
    screen.getByLabelText("Desired / current / ready replicas")
  ).toHaveTextContent("2 / 1 / 0")
  expect(screen.getByLabelText("CPU utilization")).toHaveTextContent("0%")
  expect(screen.getByText("Stale observation")).toBeInTheDocument()
  expect(
    screen.getByText("Partial: 1 of 2 components observed")
  ).toBeInTheDocument()
  expect(screen.getByRole("time")).toHaveAttribute(
    "datetime",
    "2026-11-01T12:00:00Z"
  )
})
