import { useRef, useState } from "react"
import { useMutation, useQuery } from "@/client/hooks"
import {
  useNativeRecoverySourcesPreviewRetention,
  useNativeRecoveryRetirementsCreate,
  useNativeRecoveryRetirementsList,
  useNativeRecoveryRetirementsGet,
  useNativeRecoveryRetirementsRetry,
  useNativeRecoveryRetirementsHistory,
} from "@/client/forge/loomplane/hooks"
import { useOrganization } from "@/lib/organization"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { formatDate } from "@/lib/format"
import { useDeletionRequest } from "@/components/backup-retirement/shared"
import { NativeEmpty } from "@/components/native-recovery-empty"
import { NativeRetentionPolicyControls } from "@/components/native-retention-policy"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
} from "@/components/ui/alert-dialog"

type Props = {
  source: string
  project: string
  onChanged: () => Promise<unknown>
}
export function NativeRetention(props: Props) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Review native retention
      </Button>
      {open && (
        <NativeRetentionReview {...props} onClose={() => setOpen(false)} />
      )}
    </>
  )
}
function NativeRetentionReview({
  source,
  project,
  onChanged,
  onClose,
}: Props & { onClose: () => void }) {
  const { can } = useOrganization()
  const [days, setDays] = useState("30"),
    [keep, setKeep] = useState("7")
  const [settings, setSettings] = useState({ days: 30, keep: 7 })
  const [confirming, setConfirming] = useState("")
  const [details, setDetails] = useState("")
  const [page, setPage] = useState(0)
  const preview = useQuery(
    useNativeRecoverySourcesPreviewRetention,
    {
      path: { id: source },
      query: { older_than_days: settings.days, keep_last: settings.keep },
    },
    { staleTime: 0 }
  )
  const create = useMutation(useNativeRecoveryRetirementsCreate)
  const request = useDeletionRequest(project, onChanged)
  const owner = can("service_backup:retire")
  const valid =
    Number.isInteger(Number(days)) &&
    Number(days) >= 1 &&
    Number(days) <= 3650 &&
    Number.isInteger(Number(keep)) &&
    Number(keep) >= 1 &&
    Number(keep) <= 1000
  const stale = Number(days) !== settings.days || Number(keep) !== settings.keep
  const fresh =
    valid &&
    !stale &&
    preview.status === "success" &&
    !preview.isFetching &&
    !!preview.data?.complete
  useRecoveryPolling(false, preview.refetch)
  async function remove() {
    if (!confirming || !owner || !fresh || request.busy) return
    const job = await request.run(
      `native:${source}:${confirming}:${settings.days}:${settings.keep}`,
      (requestId) =>
        create.mutateAsync({
          path: { id: source, pointId: confirming },
          body: {
            requestId,
            acknowledgeDeletion: true,
            policy: { olderThanDays: settings.days, keepLast: settings.keep },
          },
        })
    )
    if (job) {
      setConfirming("")
      setDetails(job.id)
      await preview.refetch().catch(() => undefined)
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(value) => {
        if (!value && !request.busy) onClose()
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Native backup retention</DialogTitle>
          <DialogDescription>
            Only independent full backups can be deleted. A newer SQL-verified
            point stays protected. Automated chains and PITR logs stay with the
            provider.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap items-end gap-2">
          <label className="grid gap-1.5 text-[12.5px] font-medium">
            Older than (days)
            <Input
              type="number"
              min={1}
              max={3650}
              value={days}
              className="h-8 w-32"
              onChange={(e) => setDays(e.target.value)}
            />
          </label>
          <label className="grid gap-1.5 text-[12.5px] font-medium">
            Keep newest
            <Input
              type="number"
              min={1}
              max={1000}
              value={keep}
              className="h-8 w-28"
              onChange={(e) => setKeep(e.target.value)}
            />
          </label>
          <Button
            size="sm"
            variant="outline"
            disabled={!valid || preview.isFetching}
            onClick={() => {
              setSettings({ days: Number(days), keep: Number(keep) })
              setPage(0)
              if (!stale) void preview.refetch().catch(() => undefined)
            }}
          >
            Update native preview
          </Button>
        </div>
        {stale && (
          <p className="text-xs text-muted-foreground">
            Update the preview before deleting with these settings.
          </p>
        )}
        {preview.status === "error" && (
          <QueryError
            error={preview.error}
            fallback="Could not read native retention"
            retry={preview.refetch}
          />
        )}
        {preview.status === "pending" && !preview.data ? (
          <p role="status">Reading native retention…</p>
        ) : (
          preview.data && (
            <>
              <p className="text-xs text-muted-foreground">
                {preview.data.complete
                  ? `${preview.data.eligibleCount} eligible points`
                  : "Incomplete inventory. No native deletion is allowed."}{" "}
                ·{" "}
                {preview.data.verifiedBackupId
                  ? `SQL-verified witness ${preview.data.verifiedBackupId}`
                  : "No SQL-verified witness"}
              </p>
              {preview.data.points.length ? (
                <ul
                  aria-label="Native retention decisions"
                  className="divide-y"
                >
                  {preview.data.points
                    .slice(page * 25, (page + 1) * 25)
                    .map((point) => (
                      <li
                        key={point.backupId}
                        className="flex min-w-0 flex-wrap items-center justify-between gap-2 py-2 text-xs"
                      >
                        <div className="min-w-0">
                          <p className="font-medium break-all">
                            {point.backupId}
                          </p>
                          <p className="text-muted-foreground">
                            {decision(point.reason)} ·{" "}
                            {formatDate(point.createdAt)}
                          </p>
                        </div>
                        {owner &&
                          fresh &&
                          point.eligible &&
                          point.deletionSupported && (
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={request.busy}
                              onClick={() => setConfirming(point.backupId)}
                            >
                              Delete native backup
                            </Button>
                          )}
                      </li>
                    ))}
                </ul>
              ) : (
                <NativeEmpty title="No native retention candidates">
                  Refresh the captured history and verify a newer recovery point
                  before deleting a backup.
                </NativeEmpty>
              )}
              {(page > 0 || preview.data.points.length > (page + 1) * 25) && (
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!page}
                    onClick={() => setPage(page - 1)}
                  >
                    Previous points
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={preview.data.points.length <= (page + 1) * 25}
                    onClick={() => setPage(page + 1)}
                  >
                    More points
                  </Button>
                </div>
              )}
            </>
          )
        )}
        <NativeRetentionPolicyControls
          source={source}
          project={project}
          settings={{ olderThanDays: settings.days, keepLast: settings.keep }}
          enableBlocked={
            !valid || stale
              ? "Update the preview before changing automatic native deletion."
              : preview.status !== "success" || preview.isFetching
                ? "Load a fresh native preview before enabling deletion."
                : !preview.data?.complete
                  ? "A complete native inventory is required."
                  : !preview.data.verifiedBackupId
                    ? "Verify a newer native recovery point before enabling deletion."
                    : null
          }
          onDetails={setDetails}
        />
        <NativeDeletionList source={source} onDetails={setDetails} />
        {details && (
          <NativeDeletionDetails
            key={details}
            id={details}
            project={project}
            onChanged={onChanged}
          />
        )}
        <AlertDialog
          open={!!confirming}
          onOpenChange={(value) => {
            if (!value && !request.busy) setConfirming("")
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete this native backup?</AlertDialogTitle>
              <AlertDialogDescription>
                You will permanently remove {confirming}. New restores from this
                point are blocked as soon as deletion is admitted. A lost
                response keeps the original request available for retry.
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
              <Button
                variant="destructive"
                disabled={request.busy || !owner || !fresh}
                onClick={() => void remove()}
              >
                Confirm native deletion
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </DialogContent>
    </Dialog>
  )
}
function NativeDeletionList({
  source,
  onDetails,
}: {
  source: string
  onDetails: (id: string) => void
}) {
  const [pages, setPages] = useState<string[]>([""])
  const jobs = useQuery(
    useNativeRecoveryRetirementsList,
    {
      path: { id: source },
      query: { limit: 25, before: pages.at(-1) || undefined },
    },
    { staleTime: 0 }
  )
  useRecoveryPolling(
    !!jobs.data?.retirements.some((job) =>
      ["queued", "running"].includes(job.status)
    ),
    jobs.refetch
  )
  return (
    <section
      aria-label="Native deletion history"
      className="grid min-w-0 gap-2"
    >
      <h4 className="text-[13.5px] font-semibold">Native deletions</h4>
      {jobs.status === "error" && (
        <QueryError
          error={jobs.error}
          fallback="Could not read native deletions"
          retry={jobs.refetch}
        />
      )}
      {jobs.status === "pending" && !jobs.data ? (
        <p role="status">Loading native deletions…</p>
      ) : jobs.data?.retirements.length ? (
        <ul className="divide-y">
          {jobs.data.retirements.map((job) => (
            <li
              key={job.id}
              className="flex flex-wrap items-center justify-between gap-2 py-2 text-xs"
            >
              <span className="min-w-0 break-all">
                {status(job.status)} · {job.pointId}
              </span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onDetails(job.id)}
              >
                Deletion details
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        jobs.status !== "error" && (
          <NativeEmpty title="No native deletions">
            Accepted deletions stay here after reload, including failures that
            need attention.
          </NativeEmpty>
        )
      )}
      {(pages.length > 1 || jobs.data?.nextBefore) && (
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={pages.length === 1}
            onClick={() => setPages((value) => value.slice(0, -1))}
          >
            Previous deletions
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!jobs.data?.nextBefore || jobs.status !== "success"}
            onClick={() =>
              setPages((value) => [...value, jobs.data!.nextBefore!])
            }
          >
            More deletions
          </Button>
        </div>
      )}
    </section>
  )
}
function NativeDeletionDetails({
  id,
  project,
  onChanged,
}: {
  id: string
  project: string
  onChanged: () => Promise<unknown>
}) {
  const { can } = useOrganization()
  const job = useQuery(
    useNativeRecoveryRetirementsGet,
    { path: { id } },
    { staleTime: 0 }
  )
  const [pages, setPages] = useState<string[]>([""])
  const history = useQuery(
    useNativeRecoveryRetirementsHistory,
    { path: { id }, query: { limit: 25, before: pages.at(-1) || undefined } },
    { staleTime: 0 }
  )
  const retry = useMutation(useNativeRecoveryRetirementsRetry)
  const request = useDeletionRequest(project, onChanged)
  const [confirm, setConfirm] = useState(false)
  const active = !!job.data && ["queued", "running"].includes(job.data.status)
  useRecoveryPolling(active, job.refetch)
  useRecoveryPolling(active, history.refetch)
  const busy = useRef(false)
  async function resume() {
    if (!can("service_backup:retire") || busy.current) return
    busy.current = true
    const result = await request.run(`native-retry:${id}`, () =>
      retry.mutateAsync({ path: { id } })
    )
    busy.current = false
    if (result) {
      setConfirm(false)
      await Promise.allSettled([job.refetch(), history.refetch()])
    }
  }
  return (
    <section
      aria-label="Native deletion details"
      className="grid min-w-0 gap-2 rounded-xl bg-card p-3 text-xs ring-1 ring-border"
    >
      {job.status === "error" && (
        <QueryError
          error={job.error}
          fallback="Could not read native deletion"
          retry={job.refetch}
        />
      )}
      {job.data && (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h4 className="text-[13.5px] font-semibold">
              {status(job.data.status)}
            </h4>
            {can("service_backup:retire") &&
              job.data.status === "failed" &&
              job.status === "success" && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={request.busy}
                  onClick={() => setConfirm(true)}
                >
                  Retry native deletion
                </Button>
              )}
          </div>
          <p className="break-all">
            Witness {job.data.witnessId} · Requested by {job.data.createdBy}
          </p>
          <p className="text-muted-foreground">
            Older than {job.data.policy.olderThanDays} days · keep{" "}
            {job.data.policy.keepLast}
            {job.data.policyId
              ? ` · policy ${job.data.policyId}, revision ${job.data.policyRevision}`
              : ""}
          </p>
          {job.data.error && (
            <p className="text-destructive">{job.data.error}</p>
          )}
        </>
      )}
      {history.status === "error" && (
        <QueryError
          error={history.error}
          fallback="Could not read committed native deletion history"
          retry={history.refetch}
        />
      )}
      {history.data && (
        <ul aria-label="Native deletion transitions" className="divide-y">
          {history.data.events.map((event) => (
            <li
              key={event.cursor}
              className="flex flex-wrap justify-between gap-2 py-1"
            >
              <span>{event.kind}</span>
              <span>
                {event.status} · {event.actor} · {formatDate(event.createdAt)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {(pages.length > 1 || history.data?.nextBefore) && (
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={pages.length === 1}
            onClick={() => setPages((value) => value.slice(0, -1))}
          >
            Newer events
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!history.data?.nextBefore}
            onClick={() =>
              setPages((value) => [...value, history.data!.nextBefore!])
            }
          >
            Older events
          </Button>
        </div>
      )}
      <AlertDialog
        open={confirm}
        onOpenChange={(value) => {
          if (!request.busy) setConfirm(value)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Resume the original native deletion?
            </AlertDialogTitle>
            <AlertDialogDescription>
              The original requesting owner must still have access. A previously
              uncertain delete is observed without reposting it. The point
              remains blocked from new restores.
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
            <Button
              variant="destructive"
              disabled={request.busy || !can("service_backup:retire")}
              onClick={() => void resume()}
            >
              Confirm native retry
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
function status(value: string) {
  return (
    (
      {
        queued: "Native deletion queued",
        running: "Deleting native backup",
        failed: "Native deletion needs attention",
        succeeded: "Native backup absence confirmed",
      } as Record<string, string>
    )[value] ?? value
  )
}
function decision(value: string) {
  return (
    (
      {
        eligible: "Meets age and count settings",
        provider_managed: "Provider-managed recovery chain",
        incomplete_inventory: "Incomplete inventory",
        unavailable: "Not available for deletion",
        deletion_admitted: "Deletion already admitted",
        in_use: "A recovery copy uses this point",
        witness_in_use: "Protected witness for a deletion",
        keep_last: "Kept by the minimum count",
        too_recent: "Within the age limit",
        no_newer_verified_backup: "No strictly newer SQL-verified backup",
      } as Record<string, string>
    )[value] ?? "Needs review"
  )
}
