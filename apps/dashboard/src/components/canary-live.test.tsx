import { ClientProvider } from "@forge-go/client-react"
import { QueryCache, type StreamConnection } from "@forge-go/client-core"
import { render, screen } from "@testing-library/react"
import { expect, test } from "vitest"

import { entities } from "@/client/forge/loomplane/entities"
import { CanaryPanel } from "@/components/canary"
import { LoomplaneRealtime } from "@/lib/realtime"

test.each([
  ["deployment.updated", { id: "dep_1", org_id: "org_1", status: "failed" }],
  [
    "canary_verdict.updated",
    { id: "dep_1/web/0/0", org_id: "org_1", deployment_id: "dep_1" },
  ],
])("a %s event refreshes the canary on screen", async (event, data) => {
  let report: unknown = {
    canary: {
      status: "ramping",
      steps: [5, 100],
      services: [],
      holdSeconds: 600,
      step: 0,
      weight: 5,
    },
    verdicts: [],
  }
  const cache = new QueryCache({
    entities,
    transport: { execute: async () => report },
  })
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache,
    orgId: "org_1",
    connect: () => connection,
  })
  live.start()
  render(
    <ClientProvider client={cache as never}>
      <CanaryPanel deploymentId="dep_1" />
    </ClientProvider>
  )
  expect(await screen.findByText("Ramping at 5%")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()

  // Only the status changed: no new verdict, so only the deployment moves.
  report = {
    canary: {
      status: "revert_failed",
      steps: [5, 100],
      services: [],
      holdSeconds: 600,
      step: 0,
      weight: 5,
      reason:
        "Traffic may still be split: web's candidate couldn't be removed.",
    },
    verdicts: [],
  }
  connection.message({ event, cursor: "org_1:1", data })
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Traffic may still be split"
  )
  live.stop()
})

class Connection implements StreamConnection {
  message: (frame: unknown) => void = () => {}
  onMessage(handler: (frame: unknown) => void) {
    this.message = handler
  }
  onClose() {}
  send() {}
  close() {}
}
