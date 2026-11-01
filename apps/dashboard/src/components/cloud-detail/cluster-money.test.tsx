import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"
import { ClusterMoney, exactUSD, observedTime } from "./cluster-money"
it("keeps large money exact and names unknown categories without exposing source IDs", () => {
  expect(exactUSD("9007199254740993")).toBe("$9007199254.740993")
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
  expect(screen.getByText(/Unknown \/ hour/)).toBeInTheDocument()
  expect(screen.queryByText(/private-account|opaque/)).toBeNull()
  expect(
    screen.getByText(/Known portion: \$9007199254.740993/)
  ).toBeInTheDocument()
})

it("treats absent, invalid and zero observation times as unavailable", () => {
  expect(observedTime("0001-01-01T00:00:00Z")).toBeUndefined()
  expect(observedTime("invalid")).toBeUndefined()
  expect(observedTime()).toBeUndefined()
  expect(observedTime("2026-11-01T12:00:00Z")).toBeTruthy()
})
