// A run is one plan and what came of it: planned, waiting for someone,
// approved, then deployed step by step. The dashboard's deployment views
// read runs rather than plans and deployments separately, because that's
// how people follow a change: one line from "planning" to "live".

import type {
  Action,
  Deployment,
  Plan,
  Project,
  Step,
} from "@/client/forge/loomplane/types"
import { opLabel } from "@/lib/plans"

/** How a run is doing, in the words the deployment views use. */
export type RunStatus =
  | "running"
  | "waiting"
  | "paused"
  | "blocked"
  | "approved"
  | "succeeded"
  | "failed"
  | "canceled"

/** One step of a run's thread, as the loom draws it. */
export type ThreadState =
  "done" | "running" | "waiting" | "failed" | "canceled" | "pending" | "skipped"

export interface ThreadStep {
  key: string
  name: string
  /** The phase the step belongs to; the thread draws phases as groups. */
  group: "Plan" | "Approve" | "Deploy"
  state: ThreadState
  /** How far a running step has got, 0 to 1. */
  progress?: number
  /** Said after the state in the step's tooltip, such as "Building". */
  detail?: string
  startedAt?: string
  finishedAt?: string
  /** The deployment step behind it, once there is one. */
  step?: Step
  action?: Action
}

export interface Run {
  /** The plan's ID: a run is named by the plan it starts from. */
  id: string
  plan: Plan
  deployment?: Deployment
  project?: Project
  projectId: string
  environment: string
  status: RunStatus
  /** What's happening now, or how it ended, in a few words. */
  phase: string
  createdAt: string
  finishedAt?: string
  createdBy: string
  thread: ThreadStep[]
}

export const runStatusText: Record<RunStatus, string> = {
  running: "In progress",
  waiting: "Needs approval",
  paused: "Paused for capacity",
  blocked: "Blocked",
  approved: "Approved",
  succeeded: "Succeeded",
  failed: "Failed",
  canceled: "Canceled",
}

/** isLive reports whether a run is still moving or waiting on someone. */
export function isLive(r: Run): boolean {
  return (
    r.status === "running" || r.status === "waiting" || r.status === "paused"
  )
}

// A building step is a third of the way, deploying two thirds, verifying
// nearly there: the shuttle has somewhere to sit while a step runs.
const stepProgress: Record<string, number> = {
  building: 0.3,
  deploying: 0.65,
  verifying: 0.9,
  ramping: 0.8,
}

const stepDetail: Record<string, string> = {
  pending: "Not started",
  building: "Building",
  deploying: "Deploying",
  verifying: "Verifying",
  ramping: "Taking a share of traffic",
  succeeded: "Done",
  failed: "Failed",
  skipped: "Skipped",
}

function stepState(status: string): ThreadState {
  switch (status) {
    case "succeeded":
      return "done"
    case "failed":
      return "failed"
    case "skipped":
      return "skipped"
    case "building":
    case "deploying":
    case "verifying":
    case "ramping":
      return "running"
    default:
      return "pending"
  }
}

const closed = new Set(["rejected", "expired", "superseded"])

function statusOf(plan: Plan, d?: Deployment): RunStatus {
  if (d) {
    if (d.status === "succeeded") return "succeeded"
    if (d.status === "failed") return "failed"
    // A deployment paused at a capacity limit waits for capacity or for
    // someone to resume or cancel it; it isn't in progress.
    if (d.status === "paused") return "paused"
    if (d.status === "cancelled") return "canceled"
    return "running"
  }
  switch (plan.status) {
    case "planning":
      return "running"
    case "ready":
      return "waiting"
    case "blocked":
      return "blocked"
    case "failed":
      return "failed"
    case "approved":
      // An executable plan starts its deployment a moment after approval.
      return plan.executable ? "running" : "approved"
    default:
      return closed.has(plan.status) ? "canceled" : "approved"
  }
}

/** thread lays out the plan, approval and actions that change a service. */
export function thread(plan: Plan, d?: Deployment): ThreadStep[] {
  const planning = plan.status === "planning"
  const out: ThreadStep[] = [
    {
      key: "plan",
      name: "Plan the change",
      group: "Plan",
      state: planning
        ? "running"
        : plan.status === "failed"
          ? "failed"
          : "done",
      progress: planning ? 0.5 : undefined,
      detail: planning
        ? "Working it out"
        : plan.status === "failed"
          ? "Planning failed"
          : "Planned",
      startedAt: plan.createdAt,
      finishedAt: plan.plannedAt,
    },
  ]

  const decided = plan.status === "approved" || !!d
  const approval: ThreadStep = {
    key: "approve",
    name: "Approve the plan",
    group: "Approve",
    state: decided
      ? "done"
      : plan.status === "ready"
        ? "waiting"
        : plan.status === "blocked"
          ? "failed"
          : closed.has(plan.status)
            ? "canceled"
            : "pending",
    detail: decided
      ? "Approved"
      : plan.status === "ready"
        ? "Waiting for approval"
        : plan.status === "blocked"
          ? "Blocked by problems in the plan"
          : closed.has(plan.status)
            ? plan.status[0]?.toUpperCase() + plan.status.slice(1)
            : "Not started",
    finishedAt: plan.decidedAt,
  }
  out.push(approval)

  const actions = plan.actions
    .filter((a) => a.op !== "keep")
    .sort((a, b) => a.step - b.step)
  const stepsByAction = new Map(d?.steps.map((s) => [s.actionId, s]))
  const stopped =
    closed.has(plan.status) ||
    plan.status === "failed" ||
    d?.status === "cancelled"
  if (actions.length === 0) {
    out.push({
      key: "nothing",
      name: "Nothing to change",
      group: "Deploy",
      state:
        d?.status === "succeeded" ? "done" : stopped ? "canceled" : "pending",
      detail: "What's deployed already matches",
    })
    return out
  }
  for (const a of actions) {
    const s = stepsByAction.get(a.id)
    const state = s ? stepState(s.status) : stopped ? "canceled" : "pending"
    out.push({
      key: a.id,
      name: `${opLabel(a.op)} ${a.name}`,
      group: "Deploy",
      state,
      progress: s ? stepProgress[s.status] : undefined,
      detail: s
        ? stepDetail[s.status]
        : state === "canceled"
          ? "Not run"
          : "Not started",
      startedAt: s?.startedAt,
      finishedAt: s?.finishedAt,
      step: s,
      action: a,
    })
  }
  return out
}

function phaseOf(
  status: RunStatus,
  plan: Plan,
  steps: ThreadStep[],
  d?: Deployment
): string {
  if (status === "waiting") return "Waiting for approval"
  if (status === "paused") return "Paused for capacity"
  if (d?.status === "cancelled") return "Deployment canceled"
  if (status === "blocked") return "Blocked by problems in the plan"
  if (status === "canceled") {
    return plan.status === "superseded"
      ? "Replaced by a newer plan"
      : plan.status === "expired"
        ? "Expired before anyone decided"
        : "Rejected"
  }
  if (status === "approved") return "Approved, nothing deployed"
  if (status === "succeeded") return "Live"
  const failed = steps.find((s) => s.state === "failed")
  if (status === "failed") {
    return plan.status === "failed"
      ? "Planning failed"
      : `Failed at ${failed?.name.toLowerCase() ?? "a step"}`
  }
  const now = steps.find((s) => s.state === "running")
  return now ? `${now.detail ?? "Running"}: ${now.name}` : "Starting"
}

/** toRun joins a plan with its deployment, if it has one. */
export function toRun(plan: Plan, d?: Deployment, project?: Project): Run {
  const status = statusOf(plan, d)
  const steps = thread(plan, d)
  return {
    id: plan.id,
    plan,
    deployment: d,
    project,
    projectId: plan.projectId,
    environment: plan.environment,
    status,
    phase: phaseOf(status, plan, steps, d),
    createdAt: plan.createdAt,
    // A run that stopped short of a deployment ends where it stopped: a
    // blocked plan waits for a new plan, not for anyone's approval.
    finishedAt:
      d?.finishedAt ??
      (closed.has(plan.status) || plan.status === "failed"
        ? (plan.decidedAt ?? plan.plannedAt)
        : status === "blocked"
          ? plan.plannedAt
          : status === "approved"
            ? plan.decidedAt
            : undefined),
    createdBy: plan.createdBy,
    thread: steps,
  }
}

/**
 * runKindLabel says what kind of move a plan is, in a few words: where a
 * promotion came from, or that it rolls back. A plain release says nothing.
 */
export function runKindLabel(plan: Plan): string {
  switch (plan.kind) {
    case "promotion":
      return plan.fromEnvironment
        ? `Promoted from ${plan.fromEnvironment}`
        : "Promotion"
    case "rollback":
      // A watch or a canary that failed starts one; a person starts the rest.
      return plan.rollback?.trigger && plan.rollback.trigger !== "manual"
        ? "Automatic rollback"
        : "Rollback"
  }
  return ""
}

/** elapsed is how long a run has taken so far, or took. */
export function elapsed(r: Run, now: number): number {
  const end = r.finishedAt ? Date.parse(r.finishedAt) : now
  return Math.max(0, end - Date.parse(r.createdAt))
}

/** commitLabel names the commits a run deploys, one per repository. */
export function commitLabel(r: Run): string {
  const moving = r.plan.sources.filter((s) => s.inScope)
  const shown = moving.length > 0 ? moving : r.plan.sources
  return shown
    .map((s) =>
      shown.length > 1
        ? `${s.alias}@${s.commitSha.slice(0, 7)}`
        : s.commitSha.slice(0, 7)
    )
    .join(", ")
}

/** matches reports whether a run fits a search: ID, commit, environment, project. */
export function matches(r: Run, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return [
    r.id,
    r.deployment?.id ?? "",
    r.environment,
    r.project?.name ?? "",
    r.project?.slug ?? "",
    ...r.plan.sources.flatMap((s) => [s.commitSha, s.alias, s.repository]),
  ].some((v) => v.toLowerCase().includes(q))
}

/** A line in a run's log, as the log viewer shows it. */
export interface RunLogLine {
  id: string
  ts: number
  level: "info" | "warn" | "error"
  source: string
  text: string
  step?: string
}

/**
 * runLog tells a run's story from what Loomplane recorded: the plan, its
 * findings, the decision, and each step's start and end. The build-log panel
 * combines this activity with retained provider output.
 */
export function runLog(
  r: Run,
  nameOf: (userID: string) => string
): RunLogLine[] {
  const p = r.plan
  const out: RunLogLine[] = []
  const add = (
    at: string | undefined,
    level: RunLogLine["level"],
    source: string,
    text: string,
    step?: string
  ) => {
    if (!at) return
    out.push({
      id: `${out.length}`,
      ts: Date.parse(at),
      level,
      source,
      text,
      step,
    })
  }

  add(
    p.createdAt,
    "info",
    "plan",
    `${nameOf(p.createdBy)} asked for a plan of ${r.environment} from manifest revision ${p.manifestRevision}`,
    "plan"
  )
  for (const s of p.sources) {
    add(
      p.createdAt,
      "info",
      "plan",
      `${s.inScope ? "Moves" : "Holds"} ${s.alias} at ${s.commitSha.slice(0, 7)} (${s.repository})`,
      "plan"
    )
  }
  if (p.status === "failed")
    add(
      p.plannedAt ?? p.createdAt,
      "error",
      "plan",
      p.error || "Planning failed",
      "plan"
    )
  if (p.plannedAt && p.status !== "failed") {
    const s = p.summary
    add(
      p.plannedAt,
      "info",
      "plan",
      `Planned: ${s.create} to create, ${s.update} to update, ${s.replace} to replace, ${s.delete} to delete, ${s.keep} kept`,
      "plan"
    )
  }
  for (const f of p.problems)
    add(p.plannedAt ?? p.createdAt, "error", "plan", f.message, "approve")
  for (const f of p.warnings)
    add(p.plannedAt ?? p.createdAt, "warn", "plan", f.message, "plan")

  if (p.decidedAt && p.status !== "superseded") {
    const verb =
      p.status === "approved" || r.deployment
        ? "approved"
        : p.status === "rejected"
          ? "rejected"
          : p.status
    const by = p.decidedBy ? nameOf(p.decidedBy) : "Loomplane"
    add(
      p.decidedAt,
      p.status === "rejected" ? "warn" : "info",
      "approval",
      `${by} ${verb} the plan${p.decisionNote ? `: ${p.decisionNote}` : ""}`,
      "approve"
    )
  }
  if (p.status === "superseded")
    add(
      p.decidedAt ?? p.plannedAt,
      "warn",
      "approval",
      "A newer plan of this environment replaced this one",
      "approve"
    )
  if (p.status === "expired")
    add(
      p.expiresAt,
      "warn",
      "approval",
      "Nobody decided before the plan expired",
      "approve"
    )

  const d = r.deployment
  if (d) {
    add(
      d.startedAt,
      "info",
      "deploy",
      `Deployment ${d.id} started${d.retries ? `, retry ${d.retries}` : ""}`
    )
    for (const t of r.thread.filter((s) => s.group === "Deploy" && s.step)) {
      const s = t.step!
      const where = `${s.provider}${t.action?.region ? `, ${t.action.region}` : ""}`
      add(s.startedAt, "info", s.provider, `${t.name} on ${where}`, t.key)
      if (s.image)
        add(s.startedAt, "info", s.provider, `Image ${s.image}`, t.key)
      if (s.status === "failed")
        add(
          s.finishedAt ?? s.startedAt,
          "error",
          s.provider,
          s.message || `${t.name} failed`,
          t.key
        )
      else if (s.message)
        add(s.finishedAt ?? s.startedAt, "info", s.provider, s.message, t.key)
      if (s.status === "succeeded") {
        add(
          s.finishedAt,
          "info",
          s.provider,
          s.url ? `Done, live at ${s.url}` : "Done",
          t.key
        )
      }
      if (s.status === "skipped")
        add(
          s.finishedAt ?? d.finishedAt,
          "warn",
          s.provider,
          `Skipped ${t.name.toLowerCase()} after an earlier failure`,
          t.key
        )
    }
    if (d.error) add(d.finishedAt ?? d.startedAt, "error", "deploy", d.error)
    if (d.status === "succeeded")
      add(d.finishedAt, "info", "deploy", "Deployment succeeded")
    if (d.status === "failed")
      add(d.finishedAt, "error", "deploy", "Deployment failed")
    if (d.status === "paused" && d.capacityWait)
      add(d.capacityWait.createdAt, "warn", "deploy", "Paused for capacity")
    if (d.status === "cancelled")
      add(d.finishedAt, "warn", "deploy", "Deployment canceled")
  }
  return out.sort((a, b) => a.ts - b.ts)
}

/** changesLine says in a few words what a run's plan does. */
export function changesLine(r: Run): string {
  const p = r.plan
  if (p.status === "planning") return "Working out the plan"
  if (p.status === "failed") return "Planning failed"
  if (p.problems.length > 0) {
    return `${p.problems.length} ${p.problems.length === 1 ? "problem" : "problems"} to fix`
  }
  const s = p.summary
  const parts = [
    s.create && `${s.create} to create`,
    s.update && `${s.update} to update`,
    s.replace && `${s.replace} to replace`,
    s.delete && `${s.delete} to delete`,
  ].filter(Boolean)
  return parts.length ? parts.join(", ") : "Nothing to change"
}
