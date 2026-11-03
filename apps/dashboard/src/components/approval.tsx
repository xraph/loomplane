// Deciding a waiting plan. The deployment page is the one place a plan is
// approved or rejected: its approval box says what the plan does and takes
// a note for the deploy log. The callout above the deployments list only
// points there.
import { CheckIcon, HourglassIcon, TriangleAlertIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlansApprove,
  usePlansGet,
  usePlansList,
  usePlansReject,
} from "@/client/forge/loomplane/hooks"
import type { Action, Plan } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { CloudConfirm } from "@/components/cloud-targets/cloud-page"
import { ChangeChips } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { canaryLine, canaryFindings } from "@/lib/canary"
import { dateTime, relative } from "@/lib/format"
import { useProjectCan } from "@/lib/project-permissions"
import { changeCount, planServices } from "@/lib/plans"
import { Link } from "@/lib/router"
import type { Run } from "@/lib/runs"
import { cn } from "@/lib/utils"

/** useDecision approves or rejects a run's plan, with an optional note. */
export function useDecision(run: Run) {
  const approve = useMutation(usePlansApprove)
  const reject = useMutation(usePlansReject)
  const invalidate = useInvalidate()
  const p = run.plan
  const decide = async (yes: boolean, note = "") => {
    try {
      const body = { revision: p.revision, note: note.trim() || undefined }
      const out = yes
        ? await approve.mutateAsync({ path: { id: p.id }, body })
        : await reject.mutateAsync({ path: { id: p.id }, body })
      invalidate(usePlansList)
      invalidate(usePlansGet)
      toast.success(
        out.status === "stale"
          ? "What's deployed changed since this plan was made. Plan again."
          : yes
            ? p.executable
              ? "Approved. Deploying now."
              : "Approved"
            : "Plan rejected"
      )
      return true
    } catch (err) {
      toast.error(messageOf(err, "The decision wasn't recorded."))
      return false
    }
  }
  return {
    decide,
    busy: approve.isPending || reject.isPending,
    approving: approve.isPending,
    rejecting: reject.isPending,
  }
}

/** The wash behind anything that's waiting on you. */
export const calloutClass =
  "hero-glow rounded-xl ring-1 ring-[color-mix(in_oklch,var(--weld)_22%,transparent)]"

/**
 * ApprovalCallout points whoever can decide a waiting plan at its
 * deployment page, where the plan is reviewed and decided.
 */
export function ApprovalCallout({
  run,
  waiting = 1,
  now,
  className,
}: {
  run: Run
  /** How many plans wait in all; this one is the oldest. */
  waiting?: number
  now: number
  className?: string
}) {
  const p = run.plan
  return (
    <section
      aria-label="Waiting on you"
      className={cn(
        calloutClass,
        "flex flex-wrap items-center gap-x-5 gap-y-3 px-4 py-3.5 md:px-5",
        className
      )}
    >
      <span className="flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-background text-weld-ink shadow-xs">
        <HourglassIcon className="size-[18px]" />
      </span>
      <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-1">
        <span className="text-[11.5px] font-semibold tracking-[0.06em] text-weld-ink uppercase">
          Waiting on you{waiting > 1 && ` · ${waiting}`}
        </span>
        <span className="text-[15px] font-semibold tracking-tight">
          {run.project?.name ?? "A project"} to {run.environment} needs approval
        </span>
        <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[13px] text-muted-foreground">
          <ChangeChips run={run} />
          {p.warnings.length > 0 && (
            <span className="inline-flex items-center gap-1.5 text-weld-ink">
              <TriangleAlertIcon className="size-3.5" />
              {p.warnings.length}{" "}
              {p.warnings.length === 1 ? "warning" : "warnings"}
            </span>
          )}
          <span>Planned {relative(Date.parse(run.createdAt), now)}</span>
        </span>
      </div>
      <Button
        nativeButton={false}
        render={<Link to="/deployments/$id" params={{ id: run.id }} />}
      >
        Review plan
      </Button>
    </section>
  )
}

/** listOf joins names as a person would: "a", "a and b", "a, b and c". */
function listOf(names: string[], most = 4): string {
  const shown =
    names.length > most
      ? [...names.slice(0, most - 1), `${names.length - most + 1} more`]
      : names
  if (shown.length <= 1) return shown.join("")
  return `${shown.slice(0, -1).join(", ")} and ${shown.at(-1)}`
}

const verbs: [op: string, verb: string][] = [
  ["replace", "replaces"],
  ["create", "creates"],
  ["update", "updates"],
  ["delete", "deletes"],
]

/**
 * planSentence says in one sentence what a plan does, in the order a
 * person reads it: what it replaces, creates, updates and deletes, then
 * what it keeps.
 */
export function planSentence(plan: Plan): string {
  const names = (op: string) =>
    plan.actions.filter((a: Action) => a.op === op).map((a) => a.name)
  const parts = verbs
    .map(([op, verb]) => {
      const n = names(op)
      return n.length ? `${verb} ${listOf(n)}` : ""
    })
    .filter(Boolean)
  const kept = names("keep")
  if (kept.length)
    parts.push(
      `keeps ${listOf(kept)} as ${kept.length === 1 ? "it is" : "they are"}`
    )
  if (parts.length === 0)
    return "Nothing changes: what's deployed already matches loomplane.yaml."
  const sentence =
    parts.length === 1
      ? parts[0]
      : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`
  return `${sentence.charAt(0).toUpperCase()}${sentence.slice(1)}.`
}

/** approvalTitle is the approval box's heading: what approving does. */
function approvalTitle(run: Run): string {
  const p = run.plan
  if (!p.executable) return `Approve this plan of ${run.environment}`
  const n = changeCount(p)
  const changing = p.actions.filter((a) => a.op !== "keep")
  const services =
    changing.length > 0 && changing.every((a) => a.subject === "service")
  const noun = services
    ? n === 1
      ? "service"
      : "services"
    : n === 1
      ? "change"
      : "changes"
  return n === 0
    ? `Approve to deploy ${run.environment}`
    : `Approve to deploy ${n} ${noun} to ${run.environment}`
}

/**
 * ApprovalCard is the deployment page's approval box, the only place a
 * plan is approved or rejected: what the plan does, an optional note for
 * the deploy log, then Reject or Approve and deploy.
 */
export function ApprovalCard({ run }: { run: Run }) {
  const can = useProjectCan(run.projectId)
  const { decide, busy, approving, rejecting } = useDecision(run)
  const [open, setOpen] = useState(false)
  const [note, setNote] = useState("")
  const p = run.plan
  const { ramping, release } = canaryLine(p)
  const refused = canaryFindings(p)
  const only = planServices(p)
  const warnings = p.warnings.length
  return (
    <section
      aria-label="Approve this plan"
      className="grid gap-3 rounded-xl bg-weld-tint/60 p-4 ring-1 ring-weld/30"
    >
      <div className="grid gap-1">
        <h2 className="text-[15px] font-semibold">{approvalTitle(run)}</h2>
        <p className="text-[12.5px] leading-relaxed text-muted-foreground">
          {planSentence(p)}
          {only.length > 0 &&
            ` Only ${listOf(only, 6)} ${only.length === 1 ? "moves" : "move"}; other services stay as they run.`}{" "}
          Nothing has changed in your clouds yet.
          {warnings > 0 &&
            ` ${warnings} ${warnings === 1 ? "warning is" : "warnings are"} worth a look under Changes.`}
          {p.expiresAt && ` Open until ${dateTime(Date.parse(p.expiresAt))}.`}
        </p>
        {ramping.length > 0 && (
          <p className="text-[12.5px] font-medium">
            Approving starts a canary
            <span className="font-normal text-muted-foreground">
              {" "}
              for {ramping.join(", ")}. {release}
            </span>
          </p>
        )}
        {refused.length > 0 && (
          <ul
            aria-label="Canary refusals"
            className="grid gap-1 text-[12.5px] text-weld-ink"
          >
            {refused.map((f) => (
              <li key={`${f.code}:${f.name}`} className="flex gap-1.5">
                <TriangleAlertIcon
                  aria-hidden="true"
                  className="mt-0.5 size-3.5 shrink-0"
                />
                {f.message}
              </li>
            ))}
          </ul>
        )}
      </div>
      {can("deployment:approve") ? (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            aria-label="Note for the deploy log"
            placeholder="Note for the deploy log (optional)"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            maxLength={2000}
            className="min-w-0 flex-[1_1_280px] bg-background"
          />
          <Button
            variant="outline"
            className="text-destructive"
            disabled={busy}
            onClick={() => setOpen(true)}
          >
            Reject
          </Button>
          <Button disabled={busy} onClick={() => void decide(true, note)}>
            {approving ? <Spinner /> : <CheckIcon />}
            {p.executable ? "Approve and deploy" : "Approve"}
          </Button>
        </div>
      ) : (
        <p className="text-[12.5px] text-muted-foreground">
          Someone who may approve deploys decides this plan.
        </p>
      )}
      <CloudConfirm
        open={open}
        onOpenChange={setOpen}
        title="Reject this plan?"
        confirmLabel="Reject plan"
        pending={rejecting}
        onConfirm={() =>
          void decide(false, note).then((ok) => ok && setOpen(false))
        }
      >
        Nothing changes in your clouds. Anyone can plan {run.environment} again
        once loomplane.yaml or the code is right.
        {note.trim() && " Your note goes in the deploy log."}
      </CloudConfirm>
    </section>
  )
}
