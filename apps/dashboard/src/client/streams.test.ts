import { describe, expect, it } from "vitest"

import { streams } from "@/client/forge/loomplane/stream-bindings"
import { agentTaskStream, REALTIME_EVENTS, REALTIME_WS } from "@/client/streams"
import spec from "../../specs/loomplane.openapi.json?raw"

const openapi = JSON.parse(spec) as { paths: Record<string, unknown> }

describe("stream paths", () => {
  it("are the channels the generated bindings listen on", () => {
    const channels = new Set(streams.map((b) => b.channel))
    expect(channels).toContain(REALTIME_WS)
    expect(channels).toContain(REALTIME_EVENTS)
  })

  it("name the agent task stream the API serves", () => {
    expect(openapi.paths).toHaveProperty(["/api/v1/agent/tasks/{id}/stream"])
    expect(agentTaskStream("t/1")).toBe("/api/v1/agent/tasks/t%2F1/stream")
  })
})
