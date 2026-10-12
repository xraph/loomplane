// Plans made readable: labels for statuses and actions, the order actions
// run in, and the manifest edits a plan's findings offer.

import type {
  Action,
  Manifest,
  Plan,
  Provider,
} from "@/client/forge/loomplane/types"

/** isPlanning reports whether a plan is still being worked out. */
export function isPlanning(status: string): boolean {
  return status === "planning"
}

/** isOpen reports whether a plan can still be decided or is planning. */
export function isOpen(status: string): boolean {
  return status === "planning" || status === "ready"
}

export type Tone = "ok" | "wait" | "bad" | "quiet"

const statuses: Record<string, { label: string; tone: Tone }> = {
  planning: { label: "Planning", tone: "wait" },
  ready: { label: "Waiting for approval", tone: "wait" },
  blocked: { label: "Blocked", tone: "bad" },
  approved: { label: "Approved", tone: "ok" },
  rejected: { label: "Rejected", tone: "quiet" },
  expired: { label: "Expired", tone: "quiet" },
  superseded: { label: "Replaced", tone: "quiet" },
  stale: { label: "Out of date", tone: "quiet" },
  failed: { label: "Failed", tone: "bad" },
}

/** planStatus names a plan's status for people, with how it should look. */
export function planStatus(status: string): { label: string; tone: Tone } {
  return statuses[status] ?? { label: status, tone: "quiet" }
}

const ops: Record<string, string> = {
  create: "Create",
  update: "Update",
  replace: "Replace",
  delete: "Delete",
  keep: "Keep",
}

/** opLabel names what an action does. */
export function opLabel(op: string): string {
  return ops[op] ?? op
}

/**
 * steps groups a plan's actions by step, in order. Every action in a step
 * can run at once, after the steps before it.
 */
export function steps(actions: Action[]): Action[][] {
  const by = new Map<number, Action[]>()
  for (const a of actions) {
    by.set(a.step, [...(by.get(a.step) ?? []), a])
  }
  return [...by.keys()]
    .sort((a, b) => a - b)
    .map((step) => by.get(step)!.sort((a, b) => a.name.localeCompare(b.name)))
}

/**
 * changeCount says how much a plan would change, leaving out what it
 * keeps as it is.
 */
export function changeCount(plan: Plan): number {
  const s = plan.summary
  return s.create + s.update + s.replace + s.delete
}

/**
 * withResource adds a resource of kind to a manifest, on a target that
 * provisions it: one on the cloud most of the manifest's services run on,
 * or else the first in the catalog. The name is the kind, numbered if
 * something already has it.
 */
export function withResource(
  m: Manifest,
  kind: string,
  providers: Provider[]
): Manifest {
  const services = m.services ?? {}
  const resources = m.resources ?? {}
  let name = kind
  for (let i = 2; name in services || name in resources; i++) {
    name = `${kind}-${i}`
  }
  const cloudOf = new Map(providers.map((p) => [p.name, p.cloud]))
  const clouds = new Map<string, number>()
  for (const s of Object.values(services) as { provider?: string }[]) {
    const cloud = cloudOf.get(s.provider || m.defaults?.provider || "")
    if (cloud) clouds.set(cloud, (clouds.get(cloud) ?? 0) + 1)
  }
  const able = providers.filter((p) =>
    p.provisions?.some((v) => v.kind === kind)
  )
  let best = able[0]
  for (const p of able.slice(1)) {
    if ((clouds.get(p.cloud) ?? 0) > (clouds.get(best.cloud) ?? 0)) best = p
  }
  return {
    ...m,
    resources: {
      ...resources,
      [name]: best ? { kind, provider: best.name } : { kind },
    },
  }
}

/** withIgnored adds a resource kind to what the manifest ignores. */
export function withIgnored(m: Manifest, kind: string): Manifest {
  const ignored = m.ignore?.resources ?? []
  if (ignored.includes(kind)) return m
  return { ...m, ignore: { ...m.ignore, resources: [...ignored, kind] } }
}

const deploymentStatuses: Record<string, { label: string; tone: Tone }> = {
  running: { label: "Deploying", tone: "wait" },
  succeeded: { label: "Deployed", tone: "ok" },
  failed: { label: "Failed", tone: "bad" },
}

/** deploymentStatus names a deployment's status, with how it should look. */
export function deploymentStatus(status: string): {
  label: string
  tone: Tone
} {
  return deploymentStatuses[status] ?? { label: status, tone: "quiet" }
}

const stepStatuses: Record<string, { label: string; tone: Tone }> = {
  pending: { label: "Waiting", tone: "quiet" },
  building: { label: "Building", tone: "wait" },
  deploying: { label: "Deploying", tone: "wait" },
  verifying: { label: "Checking", tone: "wait" },
  succeeded: { label: "Done", tone: "ok" },
  failed: { label: "Failed", tone: "bad" },
  skipped: { label: "Skipped", tone: "quiet" },
}

/** stepStatus names a deployment step's status, with how it should look. */
export function stepStatus(status: string): { label: string; tone: Tone } {
  return stepStatuses[status] ?? { label: status, tone: "quiet" }
}

/** isDeploying reports whether a deployment is still running. */
export function isDeploying(status: string): boolean {
  return status === "running"
}
