import { describe, expect, it } from "vitest"

import type {
  CapacityPolicy,
  Pool,
  Provider,
  Target,
} from "@/client/forge/loomplane/types"
import {
  blankRule,
  check,
  draftOf,
  frontendTargets,
  growProblem,
  scalingPoolRequest,
  tighterFrom,
  type Draft,
} from "./capacity-policy"

const GIB = 2 ** 30

const saved: CapacityPolicy = {
  orgId: "o",
  cloudId: "c",
  revision: 4,
  enabled: true,
  requireExpansionApproval: true,
  limits: {
    servers: 4,
    cpuMillis: 16000,
    memoryBytes: 32 * GIB,
    storageBytes: 400e9,
  },
  gpuLimits: [{ kind: "nvidia-l4", maximum: 2 }],
  pools: [],
  frontendLimits: [],
}
const never: CapacityPolicy = {
  ...saved,
  revision: 0,
  enabled: false,
  limits: { servers: 0, cpuMillis: 0, memoryBytes: 0, storageBytes: 0 },
  gpuLimits: [],
}
const gpuPool = { id: "pool_l4", name: "l4", acceleratorKind: "nvidia-l4" }
const cpuPool = { id: "pool_web", name: "web" }

const rule = (extra: Partial<ReturnType<typeof blankRule>> = {}) => ({
  ...blankRule(),
  connectionId: "do",
  region: "nyc2",
  image: "ubuntu",
  allowedSizes: ["s-2"],
  ...extra,
})

describe("check", () => {
  it("never turns an empty ceiling into 0", () => {
    const d: Draft = { ...draftOf(never), approval: false }
    d.ceilings = { ...d.ceilings, servers: "3", cpuMillis: "8" }
    const r = check(d, never, [], [])
    expect(r.policy).toBeUndefined()
    expect(r.problems.join(" ")).toMatch(/before you save/)
  })

  it("names the limits still missing before pools may grow", () => {
    const d: Draft = { ...draftOf(never), enabled: true }
    d.ceilings = { ...d.ceilings, servers: "2", memoryBytes: "0" }
    expect(check(d, never, [], []).problems).toContain(
      "To let pools grow, set the most this Cloud may hold for vCPU, memory, storage."
    )
  })

  it("saves a typed 0 and keeps untouched values exact", () => {
    const odd = { ...saved, limits: { ...saved.limits, memoryBytes: 12345 } }
    const d = { ...draftOf(odd), enabled: false }
    d.ceilings = { ...d.ceilings, servers: "0" }
    const r = check(d, odd, [], [])
    expect(r.policy?.limits).toEqual({
      servers: 0,
      cpuMillis: 16000,
      memoryBytes: 12345,
      storageBytes: 400e9,
    })
    expect(r.policy?.revision).toBe(4)
  })

  it("leaves out an emptied GPU ceiling instead of sending 0", () => {
    const d = draftOf(saved)
    d.gpus = {}
    expect(
      check({ ...d, enabled: false }, saved, [], []).policy?.gpuLimits
    ).toEqual([])
  })

  it("asks for a GPU ceiling for a GPU pool that may grow", () => {
    const d = draftOf({ ...saved, gpuLimits: [] })
    d.rules = { pool_l4: rule({ image: "gpu-base", allowedSizes: ["gpu-l4"] }) }
    expect(check(d, saved, [gpuPool], []).problems).toContain(
      "A GPU pool needs a GPU ceiling for NVIDIA L4."
    )
  })

  it("sends no GPU kind on a pool GPU ceiling of 0", () => {
    const d = draftOf(saved)
    d.rules = {
      pool_l4: rule({
        limits: { ...tighterFrom(d.ceilings, "0") },
      }),
    }
    const limits = check(d, saved, [gpuPool], []).policy?.pools[0]?.limits
    expect(limits).toEqual({
      servers: 4,
      cpuMillis: 16000,
      memoryBytes: 32 * GIB,
      storageBytes: 400e9,
    })
  })

  it("asks for the pool's other ceilings beside a pool GPU ceiling", () => {
    const d = draftOf(saved)
    d.rules = {
      pool_l4: rule({
        limits: { ...tighterFrom(d.ceilings, "1"), cpuMillis: "0" },
      }),
    }
    expect(check(d, saved, [gpuPool], []).problems).toContain(
      "A pool GPU ceiling needs the pool's CPU ceiling too. Copy the Cloud's ceilings to keep them as they are."
    )
  })

  it("keeps a pool under the Cloud's limits", () => {
    const d = draftOf(saved)
    d.rules = {
      pool_web: rule({
        maxServers: "9",
        limits: { ...tighterFrom(d.ceilings, ""), servers: "5" },
      }),
    }
    const problems = check(d, saved, [cpuPool], []).problems
    expect(problems).toContain(
      "web may grow to more servers than the Cloud allows."
    )
    expect(problems).toContain("web's own limits can't be above the Cloud's.")
  })

  it("drops rules for pools that are gone and limits for targets that are gone", () => {
    const withGone: CapacityPolicy = {
      ...saved,
      pools: [
        { ...rule(), poolId: "gone", minServers: 0, maxServers: 1 } as never,
      ],
      frontendLimits: [{ targetId: "deleted", maximum: 3 }],
    }
    const r = check(draftOf(withGone), withGone, [cpuPool], [])
    expect(r.policy?.pools).toEqual([])
    expect(r.policy?.frontendLimits).toEqual([])
  })
})

describe("frontendTargets", () => {
  it("offers frontend limits only on managed web app and static site targets", () => {
    const providers = [
      { name: "vercel", runs: [{ workload: "web-app" }] },
      { name: "rds", runs: [{ workload: "database" }] },
    ] as unknown as Provider[]
    const targets = [
      { id: "site", provider: "vercel", runtime: "managed" },
      { id: "db", provider: "rds", runtime: "managed" },
      { id: "box", provider: "vercel", runtime: "server" },
    ] as unknown as Target[]
    expect(frontendTargets(targets, providers).map((t) => t.id)).toEqual([
      "site",
    ])
  })
})

describe("scaling pools", () => {
  const pools: Pool[] = []
  it("waits for four Cloud limits above zero, then for a GPU ceiling", () => {
    expect(growProblem(never, "")).toMatch(/needs saved limits above zero/)
    expect(
      growProblem({ ...saved, limits: { ...saved.limits, servers: 0 } }, "")
    ).toMatch(/needs saved limits above zero/)
    expect(growProblem(saved, "nvidia-h100-80gb")).toBe(
      "A GPU pool needs a GPU ceiling for NVIDIA H100 80GB. Add one under Growth policy and save it first."
    )
    expect(growProblem(saved, "nvidia-l4")).toBe("")
  })

  it("builds the policy turned on and the new pool's template", () => {
    const r = scalingPoolRequest({
      policy: { ...saved, enabled: false },
      pools,
      targets: [],
      providers: [],
      name: "batch",
      kind: "nvidia-l4",
      rule: rule({ image: "gpu-base", allowedSizes: ["gpu-l4"] }),
    })
    expect(r.problems).toEqual([])
    expect(r.policy?.enabled).toBe(true)
    expect(r.template).toMatchObject({
      poolId: "",
      acceleratorKind: "nvidia-l4",
      allowedSizes: ["gpu-l4"],
      limits: null,
    })
  })

  it("says where to finish another pool's rule", () => {
    const other: CapacityPolicy = {
      ...saved,
      pools: [
        {
          ...rule(),
          poolId: "pool_web",
          maxServers: 9,
          minServers: 0,
        } as never,
      ],
    }
    const r = scalingPoolRequest({
      policy: other,
      pools: [cpuPool as Pool],
      targets: [],
      providers: [],
      name: "batch",
      kind: "",
      rule: rule(),
    })
    expect(r.problems).toContain(
      "web may grow to more servers than the Cloud allows. Fix it under Growth policy or in that pool's rule, then save the policy."
    )
  })
})
