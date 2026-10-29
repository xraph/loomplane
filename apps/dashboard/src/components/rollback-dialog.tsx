// Roll back: plan an environment back to the release before its newest
// one. The rollback is a plan like any other, so it waits for someone to
// approve it, and it never deletes, replaces or reverts a resource that
// holds data, even one the release it undoes created.
import { ArrowRightIcon } from "@phosphor-icons/react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlansList,
  useRollbacksCreate,
  useRollbacksTarget,
} from "@/client/forge/loomplane/hooks"
import type { RollbackTarget } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { dateTime, relative } from "@/lib/format"
import { useNavigate } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

export function RollbackDialog({
  open,
  onOpenChange,
  projectId,
  environment,
  deploymentId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: string
  environment: string
  /** The deployment to undo: the environment's newest. */
  deploymentId: string
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {/* Mounted only while open, so the target is read fresh each time. */}
        {open && (
          <RollbackForm
            projectId={projectId}
            environment={environment}
            deploymentId={deploymentId}
            done={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function RollbackForm({
  projectId,
  environment,
  deploymentId,
  done,
}: {
  projectId: string
  environment: string
  deploymentId: string
  done: () => void
}) {
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const create = useMutation(useRollbacksCreate)
  const target = useQuery(useRollbacksTarget, {
    path: { id: projectId, environment },
    query: { deployment_id: deploymentId },
  })
  const [reason, setReason] = useState("")
  const [error, setError] = useState("")
  const ready =
    target.data !== undefined && reason.trim() !== "" && !create.isPending

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    setError("")
    try {
      const p = await create.mutateAsync({
        path: { id: projectId },
        body: {
          environment,
          reason: reason.trim(),
          deploymentId,
        },
      })
      invalidate(usePlansList)
      done()
      void navigate({ to: "/deployments/$id", params: { id: p.id } })
      toast.success(`Planning a rollback of ${environment}`, {
        description: "Review the plan, then approve it to roll back.",
      })
    } catch (err) {
      setError(messageOf(err, "The rollback couldn't be planned."))
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Roll back {environment}</DialogTitle>
        <DialogDescription>
          Loomplane plans {environment} back to its previous release. Someone
          approves the plan before anything changes, and resources that hold
          data stay as they are.
        </DialogDescription>
      </DialogHeader>
      {target.status === "error" ? (
        <Alert variant="destructive">
          <AlertTitle>{environment} can&apos;t be rolled back now</AlertTitle>
          <AlertDescription>
            {messageOf(
              target.error,
              "The release to restore couldn't be read."
            )}
          </AlertDescription>
        </Alert>
      ) : !target.data ? (
        <Skeleton className="h-24 w-full rounded-lg" />
      ) : (
        <Restores target={target.data} />
      )}
      <label className="grid gap-1.5 text-[12.5px] font-medium text-muted-foreground">
        Why
        <Textarea
          aria-label="Why"
          rows={3}
          placeholder="What went wrong, for the audit trail and whoever approves it"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          maxLength={2000}
          disabled={target.status === "error"}
        />
      </label>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={done}>
          Cancel
        </Button>
        <Button type="submit" variant="destructive" disabled={!ready}>
          {create.isPending && <Spinner />}
          Plan the rollback
        </Button>
      </DialogFooter>
    </form>
  )
}

/** Restores says which release comes back and what each repository does. */
function Restores({ target: t }: { target: RollbackTarget }) {
  const now = useNow()
  const at = Date.parse(t.restore.finishedAt)
  const moving = t.commits.filter((c) => c.from !== c.to)
  return (
    <div className="grid gap-2">
      <p className="text-[13px] font-medium" title={dateTime(at)}>
        Roll {t.environment} back to the release from {relative(at, now)}
      </p>
      {moving.length > 0 ? (
        <ul
          aria-label="Commits"
          className="grid divide-y rounded-lg ring-1 ring-border"
        >
          {moving.map((c) => (
            <li
              key={c.alias}
              className="flex items-center gap-2 px-3 py-2 text-[13px]"
            >
              <span className="min-w-0 flex-1 truncate font-medium">
                {c.alias}
              </span>
              {c.from ? (
                <code className="font-mono text-[12px] text-muted-foreground">
                  {c.from.slice(0, 7)}
                </code>
              ) : (
                <span className="text-[12.5px] text-muted-foreground">new</span>
              )}
              <ArrowRightIcon
                aria-hidden="true"
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <code className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[12px]">
                {c.to.slice(0, 7)}
              </code>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] text-muted-foreground">
          The commits stay the same; the services go back to how that release
          configured them.
        </p>
      )}
    </div>
  )
}
