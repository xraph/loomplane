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

it.each([
  ["partial", "Partially fresh observations"],
  ["historical", "Historical observations"],
  ["unknown", "Observation unavailable"],
])("labels %s observations", (freshness, label) => {
  render(
    <Observations
      summary={{
        freshness,
        partial: true,
        coverage: { expected: 2, observed: 1, fresh: 1 },
      }}
    />
  )
  expect(screen.getByText(label)).toBeInTheDocument()
})
it("labels CPU and replicas from incomplete component coverage", () => {
  render(
    <Observations
      summary={{
        desiredReplicas: 2,
        currentReplicas: 1,
        readyReplicas: 1,
        cpuUtilization: 0.5,
        freshness: "fresh",
        partial: true,
        coverage: { expected: 3, observed: 3, fresh: 3, cpu: 1, replicas: 2 },
      }}
    />
  )
  expect(
    screen.getByLabelText("CPU utilization").parentElement
  ).toHaveTextContent("1 of 3 components")
  expect(
    screen.getByLabelText("Desired / current / ready replicas").parentElement
  ).toHaveTextContent("2 of 3 components")
  expect(
    screen.getByText("Values cover reporting components only.")
  ).toBeInTheDocument()
})
it("keeps unknown cost unknown even if an unqualified amount exists", () => {
  render(
    <Observations
      summary={{
        costStatus: "unknown",
        estimatedHourlyMicroUsd: 1000000,
        observedCostMicroUsd: 2000000,
        freshness: "unknown",
        partial: true,
        coverage: { expected: 0, observed: 0, fresh: 0 },
      }}
    />
  )
  expect(screen.getByLabelText("Cost")).toHaveTextContent("Unknown")
  expect(screen.queryByText(/\$1|\$2/)).not.toBeInTheDocument()
})
it.each([
  ["estimated", "Estimated $1.25/hour"],
  ["observed", "Observed $2.50 in window"],
])("distinguishes %s cost", (costStatus, label) => {
  render(
    <Observations
      summary={{
        costStatus,
        estimatedHourlyMicroUsd: 1250000,
        observedCostMicroUsd: 2500000,
        freshness: "fresh",
        partial: false,
        coverage: { expected: 1, observed: 1, fresh: 1 },
      }}
    />
  )
  expect(screen.getByLabelText("Cost")).toHaveTextContent(label)
})
it("preserves measured zero cost without rounding a small positive cost to free", () => {
  const { rerender } = render(
    <Observations
      summary={{
        costStatus: "estimated",
        estimatedHourlyMicroUsd: 0,
        freshness: "fresh",
        partial: false,
        coverage: { expected: 1, observed: 1, fresh: 1 },
      }}
    />
  )
  expect(screen.getByLabelText("Cost")).toHaveTextContent(
    "Estimated $0.00/hour"
  )
  rerender(
    <Observations
      summary={{
        costStatus: "estimated",
        estimatedHourlyMicroUsd: 1,
        freshness: "fresh",
        partial: false,
        coverage: { expected: 1, observed: 1, fresh: 1 },
      }}
    />
  )
  expect(screen.getByLabelText("Cost")).toHaveTextContent(
    "Estimated <$0.01/hour"
  )
})
it("shows null-aware resource, request and latency signals with their own coverage", () => {
  render(
    <Observations
      summary={{
        partial: true,
        freshness: "partial",
        memoryBytes: 134217728,
        requests: 0,
        latencyP95Millis: null,
        cpuUsageMillicores: 250,
        cpuRequestMillicores: 500,
        coverage: {
          expected: 2,
          observed: 2,
          fresh: 1,
          cpu: 1,
          memory: 1,
          requests: 2,
          latency: 0,
        },
      }}
    />
  )
  expect(screen.getByLabelText("Memory")).toHaveTextContent("128 MiB")
  expect(screen.getByLabelText("Memory").parentElement).toHaveTextContent(
    "1 of 2 components"
  )
  expect(screen.getByLabelText("Requests in window")).toHaveTextContent("0")
  expect(
    screen.getByLabelText("Requests in window").parentElement
  ).toHaveTextContent("2 of 2 components")
  expect(screen.getByLabelText("p95 latency bucket bound")).toHaveTextContent(
    "Unknown"
  )
  expect(screen.getByLabelText("CPU used / requested")).toHaveTextContent(
    "250 / 500 mCPU"
  )
})
it.each([
  ["source_unavailable", "Observation source unavailable"],
  ["source_failed", "Observation source failed"],
  ["source_invalid", "Observation source returned invalid data"],
  ["histogram_incompatible", "Latency histograms cannot be combined"],
])("explains %s with a public reason", (reason, label) => {
  render(
    <Observations
      summary={{
        reason,
        partial: true,
        freshness: "unknown",
        coverage: { expected: 0, observed: 0, fresh: 0 },
      }}
    />
  )
  expect(screen.getByText(label)).toBeInTheDocument()
})
it("shows separate runtime and replica timestamps without exposing unknown reason text", () => {
  render(
    <Observations
      summary={{
        partial: true,
        freshness: "partial",
        reason: "private-token",
        coverage: { expected: 1, observed: 1, fresh: 1 },
        sources: [
          {
            component: "api",
            source: "kubernetes",
            observedAt: "2026-11-01T11:58:00Z",
            runtimeObservedAt: "2026-11-01T11:59:00Z",
            replicasObservedAt: "2026-11-01T12:00:00Z",
            runtimeLive: true,
            runtimeIdentity: "pod_name_window",
            reason: "source_failed",
          },
        ],
      }}
    />
  )
  expect(
    screen.getByText("Observation sources").closest("details")
  ).not.toHaveAttribute("open")
  expect(screen.getByText("api · kubernetes")).toBeInTheDocument()
  expect(screen.getByText(/Runtime sample:/)).toHaveTextContent(
    new Date("2026-11-01T11:59:00Z").toLocaleString()
  )
  expect(screen.getByText(/Replica sample:/)).toHaveTextContent(
    new Date("2026-11-01T12:00:00Z").toLocaleString()
  )
  expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
})
