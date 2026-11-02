import { useEffect, useState } from "react"
import { messageOf, statusOf } from "@/client/errors"
import { useMutation, useQuery } from "@/client/hooks"
import {
  type CleanupReview,
  useCleanupApply,
  useCleanupCancel,
  useCleanupPreview,
  useCleanupRetry,
  useFailedCleanup,
} from "@/client/failed-cleanup"
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

export function FailedDeploymentCleanup({
  id,
  canApprove,
  onChanged,
}: {
  id: string
  canApprove: boolean
  onChanged: () => Promise<unknown>
}) {
  const state = useQuery(useFailedCleanup, { path: { id } })
  const preview = useMutation(useCleanupPreview)
  const apply = useMutation(useCleanupApply)
  const cancel = useMutation(useCleanupCancel)
  const retry = useMutation(useCleanupRetry)
  const [review, setReview] = useState<CleanupReview | null>(null)
  const [error, setError] = useState("")
  const pending =
    preview.isPending || apply.isPending || retry.isPending || cancel.isPending
  const cleanup = state.data
  const managed =
    cleanup?.variant === "managed_workload" ||
    cleanup?.variant === "managed-workload"
  async function cancelCleanup() {
    if (!cleanup?.reviewId) return
    setError("")
    try {
      await cancel.mutateAsync({
        path: { id },
        body: { expectedReviewId: cleanup.reviewId },
      })
      await Promise.all([state.refetch(), onChanged()])
    } catch (e) {
      setError(
        messageOf(
          e,
          "Cleanup cancellation failed. Refresh the current review before trying again."
        )
      )
      try {
        await state.refetch()
      } catch {
        /* The query renders its read error. */
      }
    }
  }
  const refetch = state.refetch
  // Cleanup has its own durable record and does not emit deployment events.
  useEffect(() => {
    if (cleanup?.status !== "pending") return
    const timer = window.setInterval(
      () => void refetch().catch(() => undefined),
      2000
    )
    return () => window.clearInterval(timer)
  }, [cleanup?.status, refetch])
  async function reviewCleanup() {
    setError("")
    try {
      setReview(await preview.mutateAsync({ path: { id } }))
    } catch (e) {
      setError(messageOf(e, "Cleanup could not be reviewed."))
    }
  }
  async function accept() {
    if (!review) return
    setError("")
    try {
      await apply.mutateAsync({
        path: { id },
        body: { revision: review.revision },
      })
      setReview(null)
      await Promise.all([state.refetch(), onChanged()])
    } catch (e) {
      setError(
        messageOf(
          e,
          "Cleanup could not start. Review it again before retrying."
        )
      )
      if (statusOf(e) === 409) {
        setReview(null)
        try {
          await state.refetch()
        } catch {
          /* The query renders its read error. */
        }
      }
    }
  }
  async function retryCleanup() {
    setError("")
    try {
      await retry.mutateAsync({ path: { id } })
      await state.refetch()
    } catch (e) {
      setError(messageOf(e, "Cleanup could not be retried."))
    }
  }
  const readError = state.status === "error" && statusOf(state.error) !== 404
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3 text-sm">
        <div className="min-w-0">
          <p className="font-medium">
            {cleanup?.status === "succeeded"
              ? "Deployment cleaned up"
              : cleanup
                ? "Deployment cleanup"
                : "Deployment resources"}
          </p>
          <p className="text-muted-foreground">
            {cleanup?.status === "pending"
              ? "Cleanup is running. Deployment retries are disabled."
              : cleanup?.status === "cancelled"
                ? "Cleanup review cancelled. Review again to start new attempts."
                : cleanup?.status === "failed"
                  ? "Cleanup stopped. Its progress is saved for retry."
                  : cleanup?.status === "succeeded"
                    ? managed
                      ? "Reviewed owned objects were removed. Capacity release depends on a newer complete absence observation."
                      : "The instance and its owned endpoint were removed. Deployment retries remain disabled."
                    : "Review removal if you want to abandon this failed or cancelled deployment."}
          </p>
        </div>
        {canApprove &&
          ((!cleanup && !readError && state.status === "error") ||
            cleanup?.status === "cancelled") && (
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={() => void reviewCleanup()}
            >
              Review cleanup
            </Button>
          )}
        {managed && (
          <p className="text-xs text-muted-foreground">
            Cancellation stops new cleanup attempts. Resources stay reserved
            until pending changes are checked and their outcomes confirmed.
          </p>
        )}
        {canApprove &&
          managed &&
          cleanup?.reviewId &&
          ["pending", "failed"].includes(cleanup.status) && (
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={() => void cancelCleanup()}
            >
              Cancel current cleanup review
            </Button>
          )}
        {readError && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => void state.refetch()}
          >
            Retry status
          </Button>
        )}
        {canApprove && cleanup?.status === "failed" && (
          <Button
            variant="outline"
            size="sm"
            disabled={pending}
            onClick={() => void retryCleanup()}
          >
            Retry cleanup
          </Button>
        )}
        {(error || readError || cleanup?.error) && !review && (
          <Alert variant="destructive" className="w-full">
            <AlertTitle>Cleanup needs attention</AlertTitle>
            <AlertDescription>
              {error ||
                (readError
                  ? messageOf(
                      state.error,
                      "Cleanup status could not be loaded."
                    )
                  : cleanup?.error)}
            </AlertDescription>
          </Alert>
        )}
      </div>
      <Dialog
        open={review !== null}
        onOpenChange={(open) => {
          if (!open && !apply.isPending) setReview(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Abandon deployment</DialogTitle>
            <DialogDescription>{review?.effect}</DialogDescription>
          </DialogHeader>
          {review?.variant !== "managed_workload" &&
            review?.variant !== "managed-workload" && (
              <>
                <p className="text-sm break-words">
                  {[review?.resource, review?.provider, review?.region]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
                {review?.connectionId && (
                  <p className="text-xs break-all text-muted-foreground">
                    Connection: {review.connectionId}
                  </p>
                )}
              </>
            )}
          {review?.targets?.map((target) => (
            <div
              key={`${target.planId}:${target.service}`}
              className="grid gap-1 text-sm"
            >
              <p>
                {target.service} · namespace {target.namespace}
              </p>
              <p>
                {target.identityCurrent
                  ? "Current ownership checked"
                  : "Ownership needs reconciliation"}{" "}
                · {target.unresolvedWrites} unresolved writes
              </p>
              {(target.objects ?? []).map((object) => (
                <p
                  className="text-xs break-words"
                  key={`${object.resource}:${object.name}`}
                >
                  {object.resource}: {object.namespace}/{object.name}
                </p>
              ))}
            </div>
          ))}
          {review?.blockers?.map((blocker) => (
            <p role="status" className="text-sm" key={blocker}>
              {blocker.replaceAll("_", " ")}
            </p>
          ))}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={apply.isPending}
              onClick={() => setReview(null)}
            >
              Keep deployment
            </Button>
            <Button
              variant="destructive"
              disabled={apply.isPending || !!review?.blockers?.length}
              onClick={() => void accept()}
            >
              {apply.isPending ? "Starting cleanup…" : "Abandon and clean up"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
