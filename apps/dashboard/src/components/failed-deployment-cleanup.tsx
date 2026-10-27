import { useEffect, useState } from "react"
import { messageOf, statusOf } from "@/client/errors"
import { useMutation, useQuery } from "@/client/hooks"
import {
  type CleanupReview,
  useCleanupApply,
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
  const retry = useMutation(useCleanupRetry)
  const [review, setReview] = useState<CleanupReview | null>(null)
  const [error, setError] = useState("")
  const pending = preview.isPending || apply.isPending || retry.isPending
  const cleanup = state.data
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
      if (statusOf(e) === 409) setReview(null)
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
              ? "Failed deployment cleaned up"
              : cleanup
                ? "Failed deployment cleanup"
                : "Unpublished Cloud SQL resources"}
          </p>
          <p className="text-muted-foreground">
            {cleanup?.status === "pending"
              ? "Cleanup is running. Deployment retries are disabled."
              : cleanup?.status === "failed"
                ? "Cleanup stopped. Its progress is saved for retry."
                : cleanup?.status === "succeeded"
                  ? "The instance and its owned endpoint were removed. Deployment retries remain disabled."
                  : "Review removal if you want to abandon this failed deployment."}
          </p>
        </div>
        {canApprove && !cleanup && !readError && state.status === "error" && (
          <Button
            variant="outline"
            size="sm"
            disabled={pending}
            onClick={() => void reviewCleanup()}
          >
            Review cleanup
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
            <DialogTitle>Abandon failed deployment</DialogTitle>
            <DialogDescription>{review?.effect}</DialogDescription>
          </DialogHeader>
          <p className="text-sm break-words">
            {review?.resource} · {review?.provider} · {review?.region}
          </p>
          <p className="text-xs break-all text-muted-foreground">
            Connection: {review?.connectionId}
          </p>
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
              disabled={apply.isPending}
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
