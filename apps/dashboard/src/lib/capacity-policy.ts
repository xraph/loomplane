// The capacity policy as someone edits it on the Cloud page's Capacity tab:
// every limit as typed text, so an empty field stays empty and is never sent
// as 0. A saved policy is enforced even while growth is off, and 0 is a real
// limit, so check refuses to build a policy while any ceiling is empty.
import type {
  CapacityPolicy,
  FrontendLimit,
  Pool,
  PoolExpansionPolicy,
  Provider,
  ResourceVector,
  Target,
} from "@/client/forge/loomplane/types"
import { gpuTitle } from "@/lib/gpu"

/** Each ceiling in the unit people type it in: vCPU, GiB and GB. */
export const ceilings = [
  { key: "servers", label: "Servers", scale: 1, whole: true },
  { key: "cpuMillis", label: "vCPU", scale: 1000, whole: false },
  { key: "memoryBytes", label: "Memory, GiB", scale: 2 ** 30, whole: false },
  { key: "storageBytes", label: "Storage, GB", scale: 1e9, whole: false },
] as const
export type CeilingKey = (typeof ceilings)[number]["key"]
export type Ceilings = Record<CeilingKey, string>
// The server's names for the four dimensions, in its order.
const dimensionNames = ["server", "CPU", "memory", "storage"]
// What each ceiling is called in a sentence on the Capacity tab.
const ceilingNames = ["servers", "vCPU", "memory", "storage"]

export const emptyCeilings: Ceilings = {
  servers: "",
  cpuMillis: "",
  memoryBytes: "",
  storageBytes: "",
}

const noReserve: ResourceVector = {
  servers: 0,
  cpuMillis: 0,
  memoryBytes: 0,
  storageBytes: 0,
}

/** read is a typed amount: undefined while empty, NaN when it isn't zero or more. */
export function read(text: string, whole: boolean): number | undefined {
  const t = text.trim()
  if (t === "") return undefined
  const n = Number(t)
  if (!Number.isFinite(n) || n < 0 || (whole && !Number.isInteger(n)))
    return NaN
  return n
}

const scaleOf = (key: CeilingKey) => ceilings.find((c) => c.key === key)!.scale

/** toText shows an amount in the API's unit as people type it. */
export function toText(key: CeilingKey, n: number): string {
  return String(Number((n / scaleOf(key)).toFixed(3)))
}

// toApi turns typed text back into the API's unit. Text left as it was
// keeps the saved amount exactly, so saving never nudges an untouched value.
function toApi(key: CeilingKey, n: number, text: string, saved?: number) {
  if (saved !== undefined && text.trim() === toText(key, saved)) return saved
  return Math.round(n * scaleOf(key))
}

function ceilingsOf(v: ResourceVector): Ceilings {
  return {
    servers: toText("servers", v.servers),
    cpuMillis: toText("cpuMillis", v.cpuMillis),
    memoryBytes: toText("memoryBytes", v.memoryBytes),
    storageBytes: toText("storageBytes", v.storageBytes),
  }
}

/** A pool's rule as typed: how it grows and its own tighter limits. */
export type RuleDraft = {
  connectionId: string
  region: string
  architecture: string
  image: string
  bootstrapSize: string
  allowedSizes: string[]
  minServers: string
  maxServers: string
  /** null: only the Cloud's ceilings apply. */
  limits: (Ceilings & { gpus: string }) | null
  systemReserve: ResourceVector
  /** What the rule was opened from, so untouched limits stay exact. */
  saved?: PoolExpansionPolicy
}

export type FrontendDraft = { unlimited: boolean; maximum: string }

export type Draft = {
  enabled: boolean
  approval: boolean
  ceilings: Ceilings
  /** GPU ceilings by kind. An empty one is left out: that kind can't grow. */
  gpus: Record<string, string>
  rules: Record<string, RuleDraft>
  frontends: Record<string, FrontendDraft>
}

export function blankRule(): RuleDraft {
  return {
    connectionId: "",
    region: "",
    architecture: "amd64",
    image: "",
    bootstrapSize: "",
    allowedSizes: [],
    minServers: "0",
    maxServers: "1",
    limits: null,
    systemReserve: {
      ...noReserve,
      cpuMillis: 250,
      memoryBytes: 512 * 2 ** 20,
    },
  }
}

export function ruleDraftOf(p: PoolExpansionPolicy): RuleDraft {
  return {
    connectionId: p.connectionId,
    region: p.region,
    architecture: p.architecture || "amd64",
    image: p.image,
    bootstrapSize: p.bootstrapSize ?? "",
    allowedSizes: p.allowedSizes ?? [],
    minServers: String(p.minServers),
    maxServers: String(p.maxServers),
    limits: p.limits
      ? {
          ...ceilingsOf(p.limits),
          gpus: p.limits.gpus ? String(p.limits.gpus) : "",
        }
      : null,
    systemReserve: p.systemReserve ?? noReserve,
    saved: p,
  }
}

export function draftOf(policy: CapacityPolicy): Draft {
  // A policy never saved has no ceilings: its fields start empty, not at 0.
  const saved = policy.revision > 0
  return {
    enabled: policy.enabled,
    approval: policy.requireExpansionApproval,
    ceilings: saved ? ceilingsOf(policy.limits) : emptyCeilings,
    gpus: Object.fromEntries(
      (policy.gpuLimits ?? []).map((g) => [g.kind, String(g.maximum)])
    ),
    rules: Object.fromEntries(
      (policy.pools ?? []).map((p) => [p.poolId, ruleDraftOf(p)])
    ),
    frontends: Object.fromEntries(
      (policy.frontendLimits ?? []).map((f) => [
        f.targetId,
        typeof f.maximum === "number"
          ? { unlimited: false, maximum: String(f.maximum) }
          : { unlimited: true, maximum: "" },
      ])
    ),
  }
}

/**
 * tighterFrom is where a pool's own limits start when they're turned on:
 * the Cloud's ceilings and its GPU ceiling for the pool's kind. A pool limit
 * counts every dimension, so starting any of them empty or at 0 would stop
 * the pool.
 */
export function tighterFrom(
  cloud: Ceilings,
  gpu: string
): NonNullable<RuleDraft["limits"]> {
  return { ...cloud, gpus: gpu }
}

const targetName = (t: Target) => t.displayName || t.name || t.id

/**
 * frontendTargets lists the targets that can take a frontend limit: managed
 * ones whose provider runs web apps or static sites. The server refuses a
 * limit on anything else.
 */
export function frontendTargets(targets: Target[], providers: Provider[]) {
  const hosts = new Set(
    providers
      .filter((p) =>
        (p.runs ?? []).some(
          (r) => r?.workload === "web-app" || r?.workload === "static-site"
        )
      )
      .map((p) => p.name)
  )
  return targets.filter(
    (t) => (t.runtime || "managed") === "managed" && hosts.has(t.provider)
  )
}

/** A pool as check needs it. A pool being created has no ID yet. */
export type PoolRef = Pick<Pool, "id" | "name" | "acceleratorKind">

type CloudValues = {
  /** The four Cloud ceilings in API units, undefined while empty. */
  values: (number | undefined)[]
  gpus: Record<string, number | undefined>
}

/**
 * checkRule builds a pool's template from its rule, or says what's wrong.
 * It holds the rule to the Cloud's ceilings as typed now.
 */
export function checkRule(
  rule: RuleDraft,
  pool: PoolRef,
  cloud: CloudValues,
  enabled: boolean
): { template?: PoolExpansionPolicy; problems: string[] } {
  const problems: string[] = []
  let malformed = false
  const num = (text: string, whole: boolean) => {
    const n = read(text, whole)
    if (Number.isNaN(n)) malformed = true
    return n
  }
  const name = pool.name || "the new pool"
  const kind = pool.acceleratorKind ?? ""
  const min = num(rule.minServers, true)
  const max = num(rule.maxServers, true)
  if (min === undefined || max === undefined)
    problems.push(`Set the fewest and most servers for ${name}.`)
  else if (min > max)
    problems.push(`${name}'s fewest servers is above its most.`)
  else if (
    enabled &&
    typeof cloud.values[0] === "number" &&
    max > cloud.values[0]
  )
    problems.push(`${name} may grow to more servers than the Cloud allows.`)
  let limits: ResourceVector | null = null
  if (rule.limits) {
    const own = ceilings.map((c) => num(rule.limits![c.key], c.whole))
    const gpus = kind ? num(rule.limits.gpus, true) : 0
    if (own.some((v) => v === undefined) || gpus === undefined)
      problems.push(`Set all of ${name}'s own limits, or turn them off.`)
    const saved = rule.saved?.limits ?? undefined
    limits = Object.fromEntries(
      ceilings.map((c, i) => [
        c.key,
        toApi(c.key, own[i] ?? 0, rule.limits![c.key], saved?.[c.key]),
      ])
    ) as unknown as ResourceVector
    const above = ceilings.some((c, i) => {
      const cap = cloud.values[i]
      return own[i] !== undefined && cap !== undefined && limits![c.key] > cap
    })
    if (above || (kind && (gpus ?? 0) > (cloud.gpus[kind] ?? Infinity)))
      problems.push(`${name}'s own limits can't be above the Cloud's.`)
    // A pool GPU ceiling counts every dimension too, so one at zero would
    // stop the pool. The server says the same.
    const zero = ceilings.findIndex(
      (c, i) => own[i] !== undefined && limits![c.key] <= 0
    )
    if ((gpus ?? 0) > 0 && zero >= 0)
      problems.push(
        `A pool GPU ceiling needs the pool's ${dimensionNames[zero]} ceiling too. Copy the Cloud's ceilings to keep them as they are.`
      )
    // A count of 0 carries no kind: the pool then may hold no GPUs.
    if (kind && (gpus ?? 0) > 0) limits = { ...limits, gpuKind: kind, gpus }
  }
  if (malformed)
    problems.push(
      "Use zero or more for every limit, in whole numbers for servers and GPUs."
    )
  if (problems.length) return { problems }
  return {
    problems,
    template: {
      poolId: pool.id,
      ...(kind ? { acceleratorKind: kind } : {}),
      connectionId: rule.connectionId,
      region: rule.region,
      architecture: rule.architecture || "amd64",
      image: rule.image,
      bootstrapSize: rule.bootstrapSize,
      allowedSizes: rule.allowedSizes,
      minServers: min ?? 0,
      maxServers: max ?? 0,
      systemReserve: rule.systemReserve,
      // A pool without its own limits sends null: only the Cloud's apply.
      limits: limits as ResourceVector,
    },
  }
}

export const fillText =
  "Set the most this Cloud may hold for servers, vCPU, memory and storage before you save. A saved limit applies to servers you create, adopt or resize too, so 0 means none. To keep this Cloud without limits, leave the policy unsaved."

/** cloudValues reads the Cloud's typed ceilings into API units. */
export function cloudValues(d: Draft, base: CapacityPolicy) {
  const saved = base.revision > 0 ? base.limits : undefined
  let malformed = false
  const values = ceilings.map((c) => {
    const n = read(d.ceilings[c.key], c.whole)
    if (Number.isNaN(n)) {
      malformed = true
      return undefined
    }
    return n === undefined
      ? undefined
      : toApi(c.key, n, d.ceilings[c.key], saved?.[c.key])
  })
  const gpus: Record<string, number | undefined> = {}
  for (const [kind, text] of Object.entries(d.gpus)) {
    const n = read(text, true)
    if (Number.isNaN(n)) malformed = true
    else gpus[kind] = n
  }
  return { values, gpus, malformed }
}

/**
 * check builds the policy a draft would save, or says why it can't. It
 * never builds one while a Cloud ceiling is empty, so an empty field is
 * never sent as 0. Rules for pools that are gone are dropped, and frontend
 * limits are rebuilt from the targets that can take one.
 */
export function check(
  d: Draft,
  base: CapacityPolicy,
  pools: PoolRef[],
  targets: Target[]
): { policy?: CapacityPolicy; problems: string[] } {
  const problems: string[] = []
  const cloud = cloudValues(d, base)
  const { values } = cloud
  const missing = ceilingNames.filter(
    (_, i) => d.enabled && !((values[i] ?? 0) > 0)
  )
  if (missing.length)
    problems.push(
      `To let pools grow, set the most this Cloud may hold for ${missing.join(", ")}.`
    )
  else if (values.some((v) => v === undefined) && !cloud.malformed)
    problems.push(fillText)

  const templates: PoolExpansionPolicy[] = []
  for (const pool of pools) {
    const rule = d.rules[pool.id]
    if (!rule?.connectionId) continue
    const kind = pool.acceleratorKind ?? ""
    if (d.enabled && kind && !((cloud.gpus[kind] ?? 0) > 0))
      problems.push(`A GPU pool needs a GPU ceiling for ${gpuTitle(kind)}.`)
    const r = checkRule(rule, pool, cloud, d.enabled)
    problems.push(...r.problems)
    if (r.template) templates.push(r.template)
  }

  const frontendLimits: FrontendLimit[] = targets.map((target) => {
    const f = d.frontends[target.id] ?? { unlimited: true, maximum: "" }
    if (f.unlimited)
      return { targetId: target.id, maximum: null as unknown as number }
    const n = read(f.maximum, true)
    if (n === undefined)
      problems.push(
        `Set a frontend limit for ${targetName(target)}, or choose Unlimited.`
      )
    if (Number.isNaN(n)) cloud.malformed = true
    return { targetId: target.id, maximum: n ?? 0 }
  })

  if (cloud.malformed)
    problems.push(
      "Use zero or more for every limit, in whole numbers for servers, GPUs and frontends."
    )
  if (problems.length) return { problems: [...new Set(problems)] }
  return {
    problems,
    policy: {
      ...base,
      enabled: d.enabled,
      requireExpansionApproval: d.approval,
      limits: Object.fromEntries(
        ceilings.map((c, i) => [c.key, values[i]!])
      ) as unknown as ResourceVector,
      gpuLimits: Object.entries(cloud.gpus)
        .filter((e): e is [string, number] => e[1] !== undefined)
        .map(([kind, maximum]) => ({ kind, maximum })),
      pools: templates,
      frontendLimits,
    },
  }
}

/** Where someone fixes a problem the saved policy already has. */
export const fixElsewhereText =
  "Fix it under Growth policy or in that pool's rule, then save the policy."

/**
 * scalingPoolRequest builds what creating a scaling pool sends, or says why
 * it can't: the saved policy turned on and cleaned the way a save from the
 * editor cleans it (no frontend limits the server would refuse, no GPU kind
 * on a count of 0), and the new pool's template, checked against it.
 */
export function scalingPoolRequest({
  policy,
  pools,
  targets,
  providers,
  name,
  kind,
  rule,
}: {
  policy: CapacityPolicy
  pools: Pool[]
  targets: Target[]
  providers: Provider[]
  name: string
  kind: string
  rule: RuleDraft
}): {
  policy?: CapacityPolicy
  template?: PoolExpansionPolicy
  problems: string[]
} {
  const draft = { ...draftOf(policy), enabled: true }
  const hosts = frontendTargets(targets, providers)
  // A problem the saved policy has without the new pool is fixed on the
  // Capacity tab, not in this dialog, so it says where.
  const before = check(draft, policy, pools, hosts)
  const newPool: PoolRef = { id: "", name, acceleratorKind: kind || undefined }
  const own = checkRule(rule, newPool, cloudValues(draft, policy), true)
  const problems = [
    ...before.problems.map((p) => `${p} ${fixElsewhereText}`),
    ...own.problems,
  ]
  if (!rule.connectionId)
    problems.unshift(`Choose an account for ${name || "the new pool"}.`)
  else if (!rule.region || !rule.image || !rule.allowedSizes.length)
    problems.push(
      `Choose a region, an image and at least one size for ${name || "the new pool"}.`
    )
  if (problems.length || !before.policy || !own.template) return { problems }
  return { problems: [], policy: before.policy, template: own.template }
}

/**
 * growProblem says why a pool with no servers couldn't grow under the saved
 * policy: all four Cloud ceilings above 0 (a saved 0 is a hard limit) and,
 * for a GPU pool, a ceiling for its kind. "" when neither is missing.
 */
export function growProblem(policy: CapacityPolicy, kind: string): string {
  const l = policy.limits
  const four =
    policy.revision > 0 &&
    [l.servers, l.cpuMillis, l.memoryBytes, l.storageBytes].every((n) => n > 0)
  if (!four)
    return "This Cloud needs saved limits above zero for servers, vCPU, memory and storage. Set them under Growth policy and save it first."
  if (
    kind &&
    !(policy.gpuLimits ?? []).some((g) => g.kind === kind && g.maximum > 0)
  )
    return `A GPU pool needs a GPU ceiling for ${gpuTitle(kind)}. Add one under Growth policy and save it first.`
  return ""
}
