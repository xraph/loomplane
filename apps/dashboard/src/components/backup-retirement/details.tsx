import { useCallback, useEffect, useState } from "react"
import { useQuery, useMutation } from "@/client/hooks"
import {
  useServiceRetirementsGet,
  useServiceRetirementsHistory,
  useServiceRetirementsRetry,
  useServiceBackupsGet,
  useServiceBackupsPreviewRetention,
} from "@/client/forge/loomplane/hooks"
import type { Retirement } from "@/client/forge/loomplane/types"
import { useOrganization } from "@/lib/organization"
import { formatDate } from "@/lib/format"
import { useDeploymentWrites } from "@/lib/deployment-writes"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@/components/ui/alert-dialog"
import {
  RetirementEmpty,
  deletionStatus,
  deletionMessage,
  useDeletionRequest,
} from "./shared"

type Props = {
  project: string
  id: string
  onChanged: () => Promise<unknown>
  onClose: () => void
}
export function RetirementDetails(props: Props) {
  const { busy } = useDeploymentWrites(props.project)
  const record = useQuery(useServiceRetirementsGet, { path: { id: props.id } })
  return (
    <Dialog
      open
      onOpenChange={(value) => {
        if (!value && !busy) props.onClose()
      }}
    >
      <DialogContent className="sm:max-w-2xl" showCloseButton={!busy}>
        <DialogHeader>
          <DialogTitle>Backup deletion</DialogTitle>
          <DialogDescription>
            The recovery record and history remain after its backup file is
            deleted.
          </DialogDescription>
        </DialogHeader>
        {record.status === "pending" ? (
          <p role="status">Loading deletion status…</p>
        ) : record.status === "error" ? (
          <QueryError
            error={record.error}
            fallback="Could not load deletion status"
            retry={record.refetch}
          />
        ) : (
          record.data && (
            <RetirementBody
              {...props}
              record={record.data}
              refresh={record.refetch}
            />
          )
        )}
      </DialogContent>
    </Dialog>
  )
}

function RetirementBody({
  project,
  record,
  refresh,
  onChanged,
}: Props & { record: Retirement; refresh: () => Promise<unknown> }) {
  const { can, session } = useOrganization()
  const [before, setBefore] = useState("")
  const [newer, setNewer] = useState<string[]>([])
  const [confirming, setConfirming] = useState(false)
  const request = useDeletionRequest(project, onChanged)
  const retry = useMutation(useServiceRetirementsRetry)
  const history = useQuery(useServiceRetirementsHistory, {
    path: { id: record.id },
    query: { before: before || undefined, limit: 10 },
  })
  const retained = useQuery(useServiceBackupsGet, {
    path: { id: record.witnessId },
  })
  const preview = useQuery(useServiceBackupsPreviewRetention, {
    path: { id: record.backupId },
    query: {
      older_than_days: record.policy.olderThanDays,
      keep_last: record.policy.keepLast,
    },
  })
  const active = ["queued", "running"].includes(record.status)
  const { refetch: refreshHistory } = history
  const poll = useCallback(
    () => Promise.allSettled([refresh(), refreshHistory()]),
    [refresh, refreshHistory]
  )
  useRecoveryPolling(active, poll)
  useEffect(() => {
    void refreshHistory().catch(() => undefined)
    if (["succeeded", "failed"].includes(record.status))
      void onChanged().catch(() => undefined)
  }, [record.status, record.attempt, refreshHistory, onChanged])
  const supported =
    preview.data?.points.some(
      (p) => p.backupId === record.backupId && p.deletionSupported
    ) === true
  const canRetry =
    record.status === "failed" &&
    can("service_backup:retire") &&
    supported &&
    retained.data?.status === "succeeded" &&
    !!retained.data.restoredAt
  async function retryRequest() {
    if (!canRetry) return
    const accepted = await request.run(`retry:${record.id}`, (requestId) =>
      retry.mutateAsync({ path: { id: record.id }, body: { requestId } })
    )
    if (accepted) {
      setConfirming(false)
      await Promise.allSettled([refresh(), refreshHistory()])
    }
  }
  const actor = (id: string) => (id === session.userId ? "You" : id)
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Badge
          variant={record.status === "failed" ? "destructive" : "secondary"}
        >
          {deletionStatus(record.status)}
        </Badge>
        <span className="text-xs text-muted-foreground">
          Attempt {record.attempt} of {record.attemptLimit}
        </span>
      </div>
      {record.status === "failed" && record.error && (
        <Alert variant="destructive">
          <AlertDescription>{deletionMessage(record.error)}</AlertDescription>
        </Alert>
      )}
      <dl className="grid min-w-0 grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">Requested</dt>
        <dd>{formatDate(record.createdAt)}</dd>
        <dt className="text-muted-foreground">Requester</dt>
        <dd className="break-all">{actor(record.createdBy)}</dd>
        <dt className="text-muted-foreground">Execution owner</dt>
        <dd className="break-all">{actor(record.executionBy)}</dd>
      </dl>
      <div className="grid gap-1 text-[13px]">
        <h3 className="font-medium">
          {record.status === "succeeded"
            ? "Recovery point protected during deletion"
            : "Retained recovery point"}
        </h3>
        {retained.status === "error" ? (
          <QueryError
            error={retained.error}
            fallback="Could not read the retained recovery point"
            retry={retained.refetch}
          />
        ) : retained.data ? (
          <p className="text-xs text-muted-foreground">
            {formatDate(retained.data.createdAt)} ·{" "}
            {retained.data.bytes.toLocaleString()} bytes.{" "}
            {retained.data.restoredAt
              ? `Restore verified ${formatDate(retained.data.restoredAt)}.`
              : "Restore has not been verified."}{" "}
            {retained.data.status === "retired"
              ? "Its file has since been deleted by another request."
              : "Current stored bytes are checked again before a deletion attempt."}
          </p>
        ) : (
          <p role="status">Loading retained recovery point…</p>
        )}
      </div>
      {record.status === "failed" && (
        <div className="grid gap-2">
          {preview.status === "error" ? (
            <QueryError
              error={preview.error}
              fallback="Could not check storage deletion support"
              retry={preview.refetch}
            />
          ) : (
            preview.status === "success" &&
            !supported && (
              <p className="text-xs text-muted-foreground">
                Deletion is unavailable for this captured recovery point in the
                configured storage.
              </p>
            )
          )}
          {can("service_backup:retire") && (
            <Button
              size="sm"
              variant="outline"
              className="justify-self-start"
              disabled={!canRetry || request.busy}
              onClick={() => setConfirming(true)}
            >
              Retry deletion
            </Button>
          )}
        </div>
      )}
      <div className="grid min-w-0 gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-[13.5px] font-semibold">Deletion history</h3>
          <Button size="sm" variant="ghost" onClick={() => void poll()}>
            Refresh status
          </Button>
        </div>
        {history.status === "error" ? (
          <QueryError
            error={history.error}
            fallback="Could not read deletion history"
            retry={history.refetch}
          />
        ) : history.status === "pending" ? (
          <p role="status">Loading deletion history…</p>
        ) : !history.data?.events.length ? (
          <RetirementEmpty
            title="No recorded transitions"
            description="Refresh to check for committed job history."
          />
        ) : (
          <div className="divide-y rounded-xl bg-card px-3 ring-1 ring-border">
            {history.data.events.map((event) => (
              <div key={event.cursor} className="grid gap-1 py-2 text-xs">
                <div className="flex flex-wrap justify-between gap-1">
                  <span className="font-medium">{eventLabel(event.kind)}</span>
                  <span className="text-muted-foreground">
                    {formatDate(event.createdAt)}
                  </span>
                </div>
                <div className="break-all text-muted-foreground">
                  Attempt {event.attempt} · {actor(event.actor)}
                </div>
                {(event.error || event.previousError) && (
                  <p className="text-destructive">
                    {deletionMessage(event.error || event.previousError || "")}
                  </p>
                )}
              </div>
            ))}
          </div>
        )}
        {(newer.length > 0 || history.data?.nextBefore) && (
          <div className="flex justify-between gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={!newer.length || history.isFetching}
              onClick={() => {
                setBefore(newer[newer.length - 1] ?? "")
                setNewer(newer.slice(0, -1))
              }}
            >
              Newer events
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!history.data?.nextBefore || history.isFetching}
              onClick={() => {
                if (history.data?.nextBefore) {
                  setNewer([...newer, before])
                  setBefore(history.data.nextBefore)
                }
              }}
            >
              Older events
            </Button>
          </div>
        )}
      </div>
      <AlertDialog
        open={confirming}
        onOpenChange={(value) => {
          if (!request.busy) setConfirming(value)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Retry this backup deletion?</AlertDialogTitle>
            <AlertDialogDescription>
              This authorizes up to three more attempts under your current owner
              permissions. The retained recovery point will be verified again.
              The original requester stays in the history.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {request.error && (
            <Alert variant="destructive">
              <AlertDescription>{request.error}</AlertDescription>
            </Alert>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={request.busy}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={!canRetry || request.busy}
              onClick={(event) => {
                event.preventDefault()
                void retryRequest()
              }}
            >
              {request.busy ? "Requesting retry…" : "Retry deletion"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function eventLabel(kind: string) {
  return (
    (
      {
        imported: "Earlier state imported",
        requested: "Deletion requested",
        retry: "Owner authorized a retry",
        claimed: "Worker started",
        expired: "Worker stopped before reporting a result",
        failed: "Attempt failed",
        succeeded: "Backup file deleted",
      } as Record<string, string>
    )[kind] ?? kind
  )
}
