import { describe, expect, it } from "vitest"

import type { Node } from "@/client/forge/loomplane/types"
import {
  endpointBadges,
  hasPublicAddress,
  joinedNode,
  machineLabel,
  parseLabels,
  roleNote,
  statusLabel,
  trafficLabel,
} from "@/lib/nodes"

const base: Node = {
  id: "n1",
  orgId: "o1",
  name: "hetzner-1",
  createdAt: "2026-10-28T09:00:00Z",
  updatedAt: "2026-10-28T09:00:00Z",
  edgeScope: "",
  endpoints: ["docker", "probe"],
  identityGeneration: 1,
  ingressMode: "direct",
  inventory: {
    arch: "amd64",
    cpus: 8,
    diskGib: 480,
    kernel: "6.8",
    memoryMib: 32768,
    os: "linux",
  },
  joinedBy: "u1",
  joinedByKeyId: "",
  keyFingerprint: "sha256:3f9c",
  labels: {},
  protocolVersion: 1,
  revokedBy: "",
  role: "node",
  status: "online",
  version: "v1.9.0",
}

describe("statusLabel", () => {
  it("names each status", () => {
    expect(statusLabel(base)).toBe("Online")
    expect(statusLabel({ ...base, status: "pending" })).toBe(
      "Waiting to connect"
    )
    expect(statusLabel({ ...base, status: "revoked" })).toBe("Revoked")
    expect(statusLabel({ ...base, status: "draining" })).toBe("Draining")
  })
  it("gives an offline node the local time it was last seen, on a 24 hour clock", () => {
    const seen = new Date(2026, 10, 1, 14, 2).toISOString()
    expect(statusLabel({ ...base, status: "offline", lastSeenAt: seen })).toBe(
      "Offline since 14:02"
    )
    const night = new Date(2026, 10, 1, 0, 5).toISOString()
    expect(statusLabel({ ...base, status: "offline", lastSeenAt: night })).toBe(
      "Offline since 00:05"
    )
  })
  it("says just Offline when the node never reported", () => {
    expect(statusLabel({ ...base, status: "offline" })).toBe("Offline")
  })
})

describe("endpointBadges", () => {
  it("lists what the node may serve", () => {
    expect(endpointBadges(base)).toEqual(["docker", "probe"])
    expect(endpointBadges({ ...base, endpoints: [] })).toEqual([])
  })
})

describe("labels, machine and traffic", () => {
  it("parses key=value labels", () => {
    expect(parseLabels("region=home, tier = edge")).toEqual({
      ok: true,
      labels: { region: "home", tier: "edge" },
    })
    expect(parseLabels("")).toEqual({ ok: true, labels: {} })
    expect(parseLabels("region")).toMatchObject({ ok: false })
  })
  it("summarizes the machine", () => {
    expect(machineLabel(base)).toBe("linux · amd64 · 8 CPU · 32 GiB")
    expect(
      machineLabel({
        ...base,
        inventory: { ...base.inventory, cpus: 0, memoryMib: 0 },
      })
    ).toBe("linux · amd64")
  })
  it("names how traffic reaches the node", () => {
    expect(trafficLabel(base)).toBe("Direct")
    expect(trafficLabel({ ...base, ingressMode: "relay" })).toBe("Relayed")
    expect(trafficLabel({ ...base, ingressMode: "none" })).toBe("None")
    expect(trafficLabel({ ...base, role: "edge" })).toBe("Edge")
  })
  it("knows a public address when the node reported one", () => {
    expect(hasPublicAddress(base)).toBe(false)
    expect(
      hasPublicAddress({
        ...base,
        inventory: { ...base.inventory, publicAddresses: ["203.0.113.24"] },
      })
    ).toBe(true)
  })
})

describe("joinedNode", () => {
  const before = new Set(["old"])
  it("picks the first node that was not there when the token was made", () => {
    const fresh = { ...base, id: "new" }
    expect(joinedNode([{ ...base, id: "old" }, fresh], before, "u1")?.id).toBe(
      "new"
    )
  })
  it("ignores a node someone else's token brought in", () => {
    expect(
      joinedNode([{ ...base, id: "new", joinedBy: "u2" }], before, "u1")
    ).toBeUndefined()
  })
  it("ignores nodes that are already revoked", () => {
    expect(
      joinedNode([{ ...base, id: "new", status: "revoked" }], before, "u1")
    ).toBeUndefined()
  })
})

describe("roleNote", () => {
  it("names an edge by its scope", () => {
    expect(roleNote({ ...base, role: "edge", edgeScope: "org" })).toBe(
      "Edge for this organization"
    )
    expect(roleNote({ ...base, role: "edge", edgeScope: "instance" })).toBe(
      "Edge for the instance"
    )
  })
  it("says a Mac is for development, after the edge", () => {
    const mac = { ...base.inventory, os: "darwin" }
    expect(roleNote({ ...base, inventory: mac })).toBe("macOS, for development")
    expect(roleNote({ ...base, role: "edge", inventory: mac })).toBe(
      "Edge for this organization"
    )
  })
  it("has nothing to say about an ordinary node", () => {
    expect(roleNote(base)).toBeUndefined()
  })
  it("shows no traffic for a node that has not connected", () => {
    expect(trafficLabel({ ...base, status: "pending" })).toBe("None")
  })
})
