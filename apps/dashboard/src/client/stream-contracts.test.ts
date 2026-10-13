import { describe, expect, it } from "vitest"
import type { StreamBinding } from "@forge-go/client-core"

import { CODECS, decode } from "@/client/forge/loomplane/codecs"
import { streams } from "@/client/forge/loomplane/stream-bindings"

const bindings: readonly StreamBinding[] = streams

function decoder(channel: string, message: string) {
  const binding = bindings.find(
    (b) => b.kind === "entity" && b.channel === channel && b.message === message
  )
  if (!binding || binding.kind === "duplex" || !binding.decode)
    throw new Error(`Missing codec: ${message}`)
  return { intent: binding.intent, decode: binding.decode }
}

describe("generated realtime contracts", () => {
  for (const channel of ["/api/v1/realtime/ws", "/api/v1/realtime/events"]) {
    it(`decodes named deployment and log payloads on ${channel}`, () => {
      const deployment = decoder(channel, "deployment.updated")
      expect(deployment.intent).toBe("upsert")
      expect(
        deployment.decode({
          id: "dep_fixture",
          org_id: "org_fixture",
          project_id: "proj_fixture",
          plan_id: "plan_fixture",
          started_at: "2026-10-13T12:00:00Z",
          status: "running",
        })
      ).toMatchObject({
        id: "dep_fixture",
        orgId: "org_fixture",
        projectId: "proj_fixture",
        planId: "plan_fixture",
        startedAt: "2026-10-13T12:00:00Z",
        status: "running",
      })
      const log = decoder(channel, "build.log")
      expect(
        log.decode({
          id: "org_fixture:12",
          org_id: "org_fixture",
          project_id: "proj_fixture",
          deployment_id: "dep_fixture",
          action_id: "service:api",
          operation_id: "build-1",
          provider_id: "line-2",
          message: "same text",
          attempt: "2",
          stream: "stdout",
        })
      ).toMatchObject({
        id: "org_fixture:12",
        deploymentId: "dep_fixture",
        actionId: "service:api",
        operationId: "build-1",
        providerId: "line-2",
        message: "same text",
        attempt: "2",
      })
    })
  }

  it("keeps snapshot routing metadata separate from each named entity", () => {
    const payload = {
      id: "proj_fixture",
      org_id: "org_fixture",
      name: "Fixture",
    }
    const snapshot = decode(
      {
        cursor: "org_fixture:12",
        partial: false,
        records: [
          {
            id: "org_fixture:12",
            event: "project.updated",
            org_id: "org_fixture",
            project_id: "proj_fixture",
            entity_id: "proj_fixture",
            data: payload,
          },
        ],
      },
      CODECS.Snapshot
    ) as { records: { orgId: string; entityId: string; data: unknown }[] }
    expect(snapshot.records[0]).toMatchObject({
      orgId: "org_fixture",
      entityId: "proj_fixture",
    })
    expect(snapshot.records[0].data).toEqual(payload)
    const binding = decoder("/api/v1/realtime/ws", "project.updated")
    expect(binding.decode!(snapshot.records[0].data)).toMatchObject({
      id: "proj_fixture",
      orgId: "org_fixture",
    })
  })
})
