import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"

import type { DNSRecord, RouteOperation } from "@/client/forge/loomplane/types"
import { RouteOperationProgress } from "./domains"

afterEach(cleanup)

const record = (type: string, value: string): DNSRecord => ({
  id: value,
  name: "@",
  ttl: 300,
  type,
  value,
  zoneId: "zone",
})

const operation = (over: Partial<RouteOperation>) =>
  ({
    kind: "apply",
    status: "pending",
    attempt: 1,
    message: "",
    desiredRecord: record("A", "1.1.1.1"),
    ...over,
  }) as RouteOperation

it("lists every record an operation wants", () => {
  render(
    <RouteOperationProgress
      operation={operation({
        desiredRecords: [
          record("A", "1.1.1.1"),
          record("A", "1.1.1.2"),
          record("AAAA", "2606:4700:7::60"),
        ],
      })}
    />
  )
  expect(screen.getAllByText(/^DNS:/)).toHaveLength(3)
  expect(screen.getByText(/AAAA @ = 2606:4700:7::60/)).toBeTruthy()
})

it("shows the single record of a server route", () => {
  render(<RouteOperationProgress operation={operation({})} />)
  expect(screen.getAllByText(/^DNS:/)).toHaveLength(1)
})
