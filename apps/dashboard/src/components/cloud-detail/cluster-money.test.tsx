import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"
import { ClusterMoney, microCost, observedTime } from "./cluster-money"
it("prices large amounts in the standard format and names unknown categories without exposing source IDs", () => {
  expect(microCost("620000")).toBe("$0.62/hr · about $453/mo")
  expect(microCost("")).toBe("Price unknown")
  expect(microCost("not-a-number")).toBe("Price unknown")
  render(
    <ClusterMoney
      estimate={{
        known: false,
        hourlyMicroUsd: "9007199254740993",
        horizonMicroUsd: "9007199254740993",
        horizonSeconds: 3600,
        checkedAt: "",
        priceDigest: "private",
        missingComponents: ["opaque"],
        components: [
          {
            category: "network_egress",
            name: "opaque",
            source: "private-account",
            quantity: "1",
          },
        ],
      }}
    />
  )
  expect(screen.getByText("Network traffic")).toBeInTheDocument()
  expect(screen.getByText("Price unknown")).toBeInTheDocument()
  expect(screen.queryByText(/private-account|opaque/)).toBeNull()
  expect(
    screen.getByText(/Known portion: \$9007199254\.74\/hr · about \$/)
  ).toBeInTheDocument()
})

it("treats absent, invalid and zero observation times as unavailable", () => {
  expect(observedTime("0001-01-01T00:00:00Z")).toBeUndefined()
  expect(observedTime("invalid")).toBeUndefined()
  expect(observedTime()).toBeUndefined()
  expect(observedTime("2026-11-01T12:00:00Z")).toBeTruthy()
})
