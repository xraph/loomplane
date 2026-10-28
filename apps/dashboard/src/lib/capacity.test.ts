import { describe, expect, it } from "vitest"

import type { CapacityBlock } from "@/client/forge/loomplane/types"
import { stateTone } from "@/components/cloud-targets/infrastructure-ui"
import { toneOf } from "@/components/runs"
import {
  blockText,
  expansionPrice,
  formatAmount,
  gibToBytes,
  resourceLabel,
  vcpuToMillis,
} from "@/lib/capacity"

const block = (b: Partial<CapacityBlock>): CapacityBlock => ({
  cloudId: "cloud_1",
  counted: 0,
  limit: 0,
  policyRevision: 1,
  poolId: "",
  reason: "hard_limit",
  requested: 0,
  resource: "cpu_millis",
  retryable: true,
  targetId: "tgt_1",
  ...b,
})

describe("resourceLabel", () => {
  it.each([
    ["cpu_millis", "CPU"],
    ["memory_bytes", "Memory"],
    ["storage_bytes", "Storage"],
    ["servers", "Servers"],
    ["frontends", "Frontends"],
    ["gpus:nvidia-l4", "NVIDIA L4 GPUs"],
    ["gpus:acme-x1", "acme-x1 GPUs"],
    ["load_balancers", "Load balancers"],
  ])("names %s as %s", (resource, want) => {
    expect(resourceLabel(resource)).toBe(want)
  })
})

describe("formatAmount", () => {
  it("formats CPU and memory in people's units", () => {
    expect(formatAmount("cpu_millis", 4000)).toBe("4 vCPU")
    expect(formatAmount("cpu_millis", 500)).toBe("0.5 vCPU")
    expect(formatAmount("memory_bytes", 17179869184)).toBe("16 GiB")
    expect(formatAmount("storage_bytes", 536870912)).toBe("512 MiB")
    expect(formatAmount("gpus:nvidia-l4", 2)).toBe("2")
    expect(formatAmount("servers", 3)).toBe("3")
  })

  it("converts form units back to the API's", () => {
    expect(vcpuToMillis(1.5)).toBe(1500)
    expect(gibToBytes(16)).toBe(17179869184)
  })
})

describe("blockText", () => {
  it("labels GPU blocks by kind", () => {
    expect(
      blockText(
        block({
          reason: "hard_limit",
          resource: "gpus:nvidia-h100-80gb",
          limit: 1,
          counted: 1,
          requested: 1,
          poolId: "",
        })
      )
    ).toBe(
      "NVIDIA H100 80GB GPUs is at this Cloud's limit: 1 of 1 counted, 1 more needed."
    )
  })

  it("says whose limit it is and counts in people's units", () => {
    expect(
      blockText(
        block({
          resource: "cpu_millis",
          poolId: "pool_1",
          limit: 8000,
          counted: 8000,
          requested: 2000,
        })
      )
    ).toBe(
      "CPU is at this pool's limit: 8 vCPU of 8 vCPU counted, 2 vCPU more needed."
    )
  })

  it.each([
    [
      "expansion_approval",
      "",
      "Waiting for a Cloud manager to approve a new server.",
    ],
    ["provisioning", "servers", "A new server is being created."],
    [
      "provisioning",
      "gpu_runtime",
      "Waiting for the new GPU server's NVIDIA runtime.",
    ],
    [
      "stale_inventory",
      "memory_bytes",
      "Loomplane can't confirm current Memory usage, so it won't add capacity until a refresh succeeds.",
    ],
    ["provider_quota", "", "The provider account's quota is full."],
    [
      "provider_failure",
      "",
      "The provider couldn't create the server. Loomplane will retry.",
    ],
    ["placement_constraint", "", "No allowed server size fits this workload."],
    ["review_required", "", "This deployment's plan changed. Review it again."],
    // With automatic capacity off the server won't add a server: not a plan change.
    [
      "review_required",
      "rollout",
      "Automatic provisioning is off on this Loomplane server, so no server will be added. Add or adopt one yourself.",
    ],
    ["something_new", "", "something new"],
  ])("explains %s (%s)", (reason, resource, want) => {
    expect(blockText(block({ reason, resource }))).toBe(want)
  })
})

describe("expansionPrice", () => {
  it("prices a known expansion by the hour and the month", () => {
    expect(
      expansionPrice({ estimatedHourlyUsd: 3.39, hourlyPriceKnown: true })
    ).toBe("$3.39/hr, about $2,475/mo while it runs")
  })

  it("never prices an unknown expansion at $0", () => {
    expect(
      expansionPrice({
        estimatedHourlyUsd: undefined,
        hourlyPriceKnown: undefined,
      })
    ).toBe("price unknown")
    expect(
      expansionPrice({ estimatedHourlyUsd: 0, hourlyPriceKnown: false })
    ).toBe("price unknown")
  })
})

describe("tones", () => {
  it("draws expansion and pool states in fixed status tones", () => {
    expect(stateTone("awaiting_approval")).toBe("waiting")
    expect(stateTone("reserved")).toBe("flight")
    expect(stateTone("enrolling")).toBe("flight")
    expect(stateTone("blocked")).toBe("failed")
  })

  it("draws a paused run as waiting", () => {
    expect(toneOf("paused")).toBe("waiting")
  })
})
