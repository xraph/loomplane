import { describe, expect, it } from "vitest"

import type { HostView } from "@/client/forge/loomplane/types"
import {
  expiringHosts,
  hostCertificateNotes,
  hostEvidenceIssues,
  statusLabel,
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

  it("says a never-checked host is not checked yet, and nothing else", () => {
    const never = {
      ...good,
      isolation: false,
      platform: false,
      deniedPorts: [],
      perOrgVms: 0,
      checkedAt: "0001-01-01T00:00:00Z",
      protocolVersion: "",
    }
    expect(hostEvidenceIssues(never, now, 10 * 60_000)).toEqual([
      "Not checked yet",
    ])
    expect(
      hostEvidenceIssues({ ...never, checkedAt: "" }, now, 10 * 60_000)
    ).toEqual(["Not checked yet"])
    expect(
      hostEvidenceIssues(
        { ...good, protocolVersion: "0", reason: "dial tcp: refused" },
        now,
        10 * 60_000
      )
    ).toEqual(["Not checked yet", "dial tcp: refused"])
  })

  it("names an unknown suspension reason without saying undefined", () => {
    expect(suspendMessage(["quota_review"])).toBe(
      "Loomplane Cloud work is stopped because something else needs attention. It starts again when it is resolved."
    )
    expect(suspendMessage(["past_due", "quota_review"])).toBe(
      "Loomplane Cloud work is stopped because a payment is past due and something else needs attention. It starts again when both are resolved."
    )
    expect(suspendMessage(["platform_owner", "quota_review"])).toBe(
      "Loomplane Cloud work is stopped because the instance owner stopped it and something else needs attention. It starts again when both are resolved."
    )
  })

  it("labels stored statuses in plain words", () => {
    expect(statusLabel("host", "ready")).toBe("Ready")
    expect(statusLabel("host", "pending")).toBe("Not ready")
    expect(statusLabel("host", "draining")).toBe("Draining")
    expect(statusLabel("host", "retired")).toBe("Retired")
    expect(statusLabel("region", "active")).toBe("Open")
    expect(statusLabel("region", "closed")).toBe("Closed")
    expect(statusLabel("tenant", "enabled")).toBe("Enabled")
    expect(statusLabel("tenant", "disabled")).toBe("Disabled")
    expect(statusLabel("placement", "running")).toBe("Running")
    expect(statusLabel("placement", "reserved")).toBe("Starting")
    expect(statusLabel("placement", "waiting_for_room")).toBe(
      "Waiting for room"
    )
    expect(statusLabel("placement", "")).toBe("Unknown")
  })
})

describe("host certificate notes", () => {
  it("says how soon the certificate runs out, urgently inside 30 days", () => {
    expect(
      hostCertificateNotes({ warning: "expiring", daysLeft: 60, rotation: "" })
    ).toEqual([
      { text: "The client certificate expires in 60 days", urgent: false },
    ])
    expect(
      hostCertificateNotes({ warning: "urgent", daysLeft: 1, rotation: "" })
    ).toEqual([
      {
        text: "The client certificate expires in 1 day. Rotate it now.",
        urgent: true,
      },
    ])
    expect(
      hostCertificateNotes({
        warning: "expired",
        daysLeft: -2,
        rotation: "",
      })[0].urgent
    ).toBe(true)
    expect(hostCertificateNotes({ warning: "", rotation: "" })).toEqual([])
    expect(hostCertificateNotes(undefined)).toEqual([])
  })

  it("says what a rotation waits for", () => {
    expect(
      hostCertificateNotes({ warning: "", rotation: "waiting" })[0].text
    ).toMatch(/add the new certificate/)
    expect(
      hostCertificateNotes({ warning: "", rotation: "retiring" })[0].text
    ).toMatch(/remove the old certificate/)
  })

  it("counts hosts in service whose certificate needs rotating", () => {
    const h = (status: string, warning: string) =>
      ({ status, certificate: { warning, rotation: "" } }) as HostView
    expect(
      expiringHosts([
        h("ready", "urgent"),
        h("draining", "expired"),
        h("retired", "expired"),
        h("ready", ""),
      ])
    ).toBe(2)
  })
})
