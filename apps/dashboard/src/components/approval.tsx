// Deciding a waiting plan, from wherever it shows up: the callout above the
// deployments list and the overview, and the fuller card on the
// deployment's own page, which also takes a note for the record.
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
import { useInvalidate, useMutation } from "@/client/hooks"
import { ChangeChips } from "@/components/runs"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
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

function RejectDialog({
  run,
  open,
  onOpenChange,
  note,
  onNote,
  onReject,
  busy,
  rejecting,
}: {
  run: Run
  open: boolean
  onOpenChange: (open: boolean) => void
  note: string
  onNote: (note: string) => void
  onReject: () => void
  busy: boolean
  rejecting: boolean
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Reject this plan?</AlertDialogTitle>
          <AlertDialogDescription>
            Nothing changes in your clouds. Anyone can plan {run.environment}{" "}
            again once the manifest or the code is right.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <Textarea
          aria-label="Why"
          placeholder="Why, for the record (optional)"
          value={note}
          onChange={(e) => onNote(e.target.value)}
          maxLength={2000}
        />
        <AlertDialogFooter>
          <AlertDialogCancel>Keep reviewing</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={busy}
            onClick={onReject}
          >
            {rejecting && <Spinner />}
            Reject plan
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/** The wash behind anything that's waiting on you. */
export const calloutClass =
  "hero-glow rounded-xl ring-1 ring-[color-mix(in_oklch,var(--weld)_22%,transparent)]"

/** ApprovalCallout puts a waiting plan in front of whoever can decide it. */
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
  const { can } = useOrganization()
  const { decide, busy, approving } = useDecision(run)
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
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          nativeButton={false}
          render={<Link to="/deployments/$id" params={{ id: run.id }} />}
        >
          Review plan
        </Button>
        {can("deployment:approve") && (
          <Button disabled={busy} onClick={() => void decide(true)}>
            {approving ? <Spinner /> : <CheckIcon />}
            {p.executable ? "Approve and deploy" : "Approve"}
          </Button>
        )}
      </div>
    </section>
  )
}

/** ApprovalCard is the deployment page's decision, with a note to keep. */
export function ApprovalCard({ run }: { run: Run }) {
  const { can } = useOrganization()
  const { decide, busy, approving, rejecting } = useDecision(run)
  const [open, setOpen] = useState(false)
  const [note, setNote] = useState("")
  const p = run.plan
  const s = p.summary
  const removes = s.delete + s.replace
  return (
    <section
      aria-label="Approve this deploy"
      className={cn(calloutClass, "flex flex-wrap gap-x-8 gap-y-4 p-4 md:p-5")}
    >
      <div className="flex min-w-0 flex-[1_1_380px] flex-col gap-2">
        <span className="text-[11.5px] font-semibold tracking-[0.06em] text-weld-ink uppercase">
          Waiting on you
        </span>
        <h2 className="text-base font-semibold tracking-tight">
          {removes > 0
            ? `This plan replaces or removes ${removes} ${removes === 1 ? "thing" : "things"}, so it waits for an approval.`
            : `Deploying to ${run.environment} waits for someone to approve the plan.`}
        </h2>
        <p className="text-[13.5px] leading-relaxed text-muted-foreground">
          {s.create} to create, {s.update} to update, {s.replace} to replace,{" "}
          {s.delete} to delete. Nothing has changed in your clouds yet.
          {p.warnings.length > 0 &&
            ` ${p.warnings.length} ${p.warnings.length === 1 ? "warning is" : "warnings are"} worth a look in the plan.`}
        </p>
        <ChangeChips run={run} />
      </div>
      {can("deployment:approve") && (
        <div className="flex flex-[1_1_300px] flex-col gap-2">
          <label className="grid gap-1.5 text-[12.5px] font-medium text-muted-foreground">
            Note for the deploy log (optional)
            <Textarea
              rows={2}
              placeholder="Why this is safe to ship"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              maxLength={2000}
              className="bg-background"
            />
          </label>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              className="text-destructive"
              disabled={busy}
              onClick={() => setOpen(true)}
            >
              Reject
            </Button>
            <Button
              className="flex-1"
              disabled={busy}
              onClick={() => void decide(true, note)}
            >
              {approving ? <Spinner /> : <CheckIcon />}
              {p.executable ? "Approve and deploy" : "Approve"}
            </Button>
          </div>
        </div>
      )}
      <RejectDialog
        run={run}
        open={open}
        onOpenChange={setOpen}
        note={note}
        onNote={setNote}
        busy={busy}
        rejecting={rejecting}
        onReject={() =>
          void decide(false, note).then((ok) => ok && setOpen(false))
        }
      />
    </section>
  )
}
