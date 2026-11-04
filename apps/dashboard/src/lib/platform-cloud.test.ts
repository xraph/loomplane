import { describe, expect, it } from "vitest"

import {
  hostEvidenceIssues,
  suspendMessage,
  usageRows,
} from "@/lib/platform-cloud"

describe("platform cloud helpers", () => {
  it("formats use against limits", () => {
    const rows = usageRows(
      { vcpus: 4, memoryMib: 8192, vms: 10, concurrentBuilds: 1 },
      { vcpus: 3, memoryMib: 9216, vms: 2, concurrentBuilds: 0 }
    )
    expect(rows.map((r) => r.text)).toEqual([
      "3 of 4 vCPUs",
      "9 of 8 GiB memory",
      "2 of 10 VMs",
      "0 of 1 builds at once",
    ])
    expect(rows[1].over).toBe(true)
    expect(rows[0].over).toBe(false)
  })

  it("explains stacked suspensions", () => {
    expect(suspendMessage([])).toBeNull()
    expect(suspendMessage(["spending_limit", "past_due"])).toBe(
      "Loomplane Cloud work is stopped because the spending limit was reached and a payment is past due. It starts again when both are resolved."
    )
    expect(suspendMessage(["platform_owner"])).toBe(
      "Loomplane Cloud work is stopped by the instance owner. It starts again when they restore it."
    )
  })

  it("explains a single suspension reason", () => {
    expect(suspendMessage(["spending_limit"])).toBe(
      "Loomplane Cloud work is stopped because the spending limit was reached. It starts again when it is resolved."
    )
    expect(suspendMessage(["past_due"])).toBe(
      "Loomplane Cloud work is stopped because a payment is past due. It starts again when it is resolved."
    )
  })

  it("explains all three suspension reasons", () => {
    expect(
      suspendMessage(["spending_limit", "past_due", "platform_owner"])
    ).toBe(
      "Loomplane Cloud work is stopped because the spending limit was reached, a payment is past due and the instance owner stopped it. It starts again when all are resolved."
    )
  })

  const now = new Date("2026-11-03T12:00:00Z")
  const base = {
    architecture: "amd64",
    ingressAddress: "203.0.113.1",
    protocolVersion: "1",
    runtimeIdentity: "host-one",
  }
  const good = {
    ...base,
    isolation: true,
    platform: true,
    smtActive: false,
    deniedPorts: [25, 465, 587],
    perOrgVms: 1,
    perOrgVcpus: 4,
    perOrgMemoryMib: 8192,
    checkedAt: "2026-11-03T11:59:00Z",
  }

  it("names each failing host check", () => {
    expect(
      hostEvidenceIssues(
        {
          ...base,
          isolation: true,
          platform: true,
          smtActive: true,
          deniedPorts: [25, 587],
          perOrgVms: 0,
          perOrgVcpus: 4,
          perOrgMemoryMib: 8192,
          checkedAt: "2026-11-03T11:00:00Z",
        },
        now,
        10 * 60_000
      )
    ).toEqual([
      "SMT is on",
      "Mail ports aren't all closed",
      "No per-workspace limit on the host",
      "Last check is older than 10 minutes",
    ])
  })

  it("reports a healthy host as clean", () => {
    expect(hostEvidenceIssues(good, now, 10 * 60_000)).toEqual([])
  })

  it("covers isolation, platform mode, null ports, bad dates and a reason", () => {
    expect(
      hostEvidenceIssues(
        {
          ...good,
          isolation: false,
          platform: false,
          deniedPorts: null as unknown as number[],
          checkedAt: "not a date",
          reason: "Probe could not reach the host",
        },
        now,
        10 * 60_000
      )
    ).toEqual([
      "Isolation checks failed",
      "Platform mode is off",
      "Mail ports aren't all closed",
      "Last check is older than 10 minutes",
      "Probe could not reach the host",
    ])
  })
})
