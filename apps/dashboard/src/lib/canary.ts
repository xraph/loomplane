// What a canary release looks like to a person: the manifest's release in a
// line, where the ramp stands step by step, and which statuses need someone.
import type { Run } from "@/lib/runs"
import type {
  Canary,
  CanaryVerdict,
  Finding,
  Health,
  Plan,
  Release,
} from "@/client/forge/loomplane/types"

/** DefaultHoldSeconds is the hold the manifest gets when it names none. */
const defaultHoldSeconds = 600

export type StepState = {
  weight: number
  state: "pending" | "holding" | "passed" | "failed" | "inconclusive"
}

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? "" : "s"}`
}

/** holdLabel names a hold the way a person would say it. */
export function holdLabel(seconds: number): string {
  if (seconds % 3600 === 0 && seconds >= 3600) {
    return plural(seconds / 3600, "hour")
  }
  if (seconds % 60 === 0 && seconds >= 60) return plural(seconds / 60, "minute")
  return plural(seconds, "second")
}

/** releaseSummary reads like the manifest's release block. */
export function releaseSummary(release: Release): string {
  switch (release.strategy) {
    case "canary": {
      const steps = release.steps?.length ? release.steps : [5, 25, 50, 100]
      const hold = holdLabel(release.holdSeconds || defaultHoldSeconds)
      return `Canary: ${steps.map((s) => `${s}%`).join(", ")}, holding ${hold} at each step`
    }
    case "blue_green":
      return "Blue/green: the new version takes all traffic once it is healthy"
    default:
      return "Direct: the new version takes all traffic at once"
  }
}

/** needsAttention is a canary that ended with traffic that may still be split. */
export function needsAttention(canary: Canary): boolean {
  return canary.status === "revert_failed" || canary.status === "promote_failed"
}

export const statusLabel: Record<string, string> = {
  pending: "Starting",
  ramping: "Ramping",
  promoting: "Promoting",
  promoted: "Promoted",
  reverting: "Rolling back",
  rolled_back: "Rolled back",
  revert_failed: "Rollback failed",
  promote_failed: "Promote failed",
}

/** latestAt keeps each service's last verdict at a step. */
function latestAt(verdicts: CanaryVerdict[], step: number): CanaryVerdict[] {
  const byService = new Map<string, CanaryVerdict>()
  for (const v of verdicts) {
    if (v.step !== step) continue
    const cur = byService.get(v.actionId)
    if (!cur || v.attempt > cur.attempt) byService.set(v.actionId, v)
  }
  return [...byService.values()]
}

function decided(
  verdicts: CanaryVerdict[]
): "pass" | "fail" | "inconclusive" | "" {
  if (verdicts.length === 0) return ""
  if (verdicts.some((v) => v.verdict === "fail")) return "fail"
  if (verdicts.some((v) => v.verdict === "inconclusive")) return "inconclusive"
  return "pass"
}

/**
 * stoppedAt is the step a canary that ended stopped at: the first step a
 * verdict failed, or the current one when its last verdict was
 * inconclusive. It's -1 when every judged step passed.
 */
function stoppedAt(canary: Canary, verdicts: CanaryVerdict[]): number {
  const steps = canary.steps ?? []
  const failed = steps.findIndex(
    (_, i) => decided(latestAt(verdicts, i)) === "fail"
  )
  if (failed >= 0) return failed
  if (decided(latestAt(verdicts, canary.step)) === "inconclusive") {
    return canary.step
  }
  return -1
}

/**
 * stepStates marks where the ramp is: steps before the current one passed,
 * the current one holds, and later ones wait. Once the canary ends in a
 * rollback, the step it stopped at failed, or ran out of holds without a
 * verdict ("inconclusive"). A promotion that failed passed every step,
 * unless the verdicts say a step failed: a retry can fail a step after an
 * earlier attempt promoted some services, and that ends promote_failed too.
 */
export function stepStates(
  canary: Canary,
  verdicts: CanaryVerdict[]
): StepState[] {
  const steps = canary.steps ?? []
  const passed = steps.map((weight): StepState => ({ weight, state: "passed" }))
  if (canary.status === "promoting" || canary.status === "promoted") {
    return passed
  }
  let stopped: number | undefined
  if (
    canary.status === "reverting" ||
    canary.status === "rolled_back" ||
    canary.status === "revert_failed"
  ) {
    stopped = canary.step
  } else if (canary.status === "promote_failed") {
    stopped = stoppedAt(canary, verdicts)
    if (stopped < 0) return passed
  }
  return steps.map((weight, i): StepState => {
    const verdict = decided(latestAt(verdicts, i))
    if (stopped !== undefined) {
      if (i < stopped) return { weight, state: "passed" }
      if (i === stopped) {
        return {
          weight,
          state: verdict === "inconclusive" ? "inconclusive" : "failed",
        }
      }
      return { weight, state: "pending" }
    }
    if (verdict === "pass" || i < canary.step)
      return { weight, state: "passed" }
    if (i === canary.step) return { weight, state: "holding" }
    return { weight, state: "pending" }
  })
}

/** errorRate is a measurement's share of failed requests, as a percent. */
export function errorRate(h: Health): string {
  if (!h.available) return "n/a"
  if (h.requests <= 0) return "no requests"
  return `${((h.errors / h.requests) * 100).toFixed(1)}%`
}

/** latency reads a measurement's latency with the statistic it is. */
export function latency(h: Health): string {
  if (!h.available) return ""
  const stat = h.statistic === "average" ? "avg" : (h.statistic ?? "")
  return `${Math.round(h.latencyMs)}ms${stat ? ` ${stat}` : ""}`
}

const canaryCodes = new Set([
  "canary-unavailable",
  "canary-permissions",
  "canary-app-wide-change",
])

/**
 * canaryLine says which services a plan's release ramps. A service the plan
 * refused a canary for (or one that deploys all at once) isn't in the list,
 * so the approval card never claims a canary for it.
 */
export function canaryLine(plan: Plan): { ramping: string[]; release: string } {
  const r = plan.release
  if (!r || r.strategy !== "canary") return { ramping: [], release: "" }
  const ids = new Set(r.canary ?? [])
  const ramping = plan.actions.filter((a) => ids.has(a.id)).map((a) => a.name)
  return { ramping, release: `${releaseSummary(r)}.` }
}

/**
 * canaryFindings are the plan's own words on why a service deploys all at
 * once. Refusals that block the plan (permissions, app-wide changes) are
 * problems, shown by the blocked plan's alert instead.
 */
export function canaryFindings(plan: Plan): Finding[] {
  return (plan.warnings ?? []).filter((f) => canaryCodes.has(f.code))
}

/**
 * showsCanary says whether the deployment page has a canary to show. Once a
 * deployment exists, it does when the deployment has one. Before, only an
 * approvable plan whose release ramps at least one service does; a blocked
 * or rejected plan will never start a ramp.
 */
export function showsCanary(run: Run): boolean {
  if (run.deployment) return Boolean(run.deployment.canary)
  const ramps = (run.plan.release?.canary ?? []).length > 0
  return ramps && (run.status === "waiting" || run.status === "approved")
}
