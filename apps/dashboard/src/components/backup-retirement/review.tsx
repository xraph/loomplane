import { useState } from "react"
import { useMutation, useQuery } from "@/client/hooks"
import {
  useServiceBackupsPreviewRetention,
  useServiceRetirementsCreate,
} from "@/client/forge/loomplane/hooks"
import type { Backup } from "@/client/forge/loomplane/types"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { useOrganization } from "@/lib/organization"
import { formatDate } from "@/lib/format"
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
  AlertDialogAction,
} from "@/components/ui/alert-dialog"
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import {
  Table,
  TableHeader,
  TableHead,
  TableRow,
  TableBody,
  TableCell,
} from "@/components/ui/table"
import { QueryError } from "@/components/query-error"
import { RetirementDetails } from "./details"
import { RetentionPolicyControls } from "./policy"
import {
  RetirementEmpty,
  retentionDecision,
  useDeletionRequest,
} from "./shared"

type Props = {
  compact?: boolean
  project: string
  points: Backup[]
  onChanged: () => Promise<unknown>
}
export function BackupRetentionReview(props: Props) {
  const [open, setOpen] = useState(false)
  const [operation, setOperation] = useState("")
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        disabled={!props.points.length}
        onClick={() => setOpen(true)}
      >
        {props.compact ? "Retention" : "Review retention"}
      </Button>
      {open && (
        <RetentionReview
          {...props}
          onClose={() => setOpen(false)}
          onAccepted={(id) => {
            setOpen(false)
            setOperation(id)
          }}
        />
      )}
      {operation && (
        <RetirementDetails
          project={props.project}
          id={operation}
          onChanged={props.onChanged}
          onClose={() => setOperation("")}
        />
      )}
    </>
  )
}

function RetentionReview({
  project,
  points,
  onChanged,
  onClose,
  onAccepted,
}: Props & { onClose: () => void; onAccepted: (id: string) => void }) {
  const { can } = useOrganization()
  const [anchor, setAnchor] = useState(
    points.find((p) => p.status === "succeeded")?.id ?? points[0]?.id ?? ""
  )
  const [days, setDays] = useState("30")
  const [keep, setKeep] = useState("7")
  const [settings, setSettings] = useState({ days: 30, keep: 7 })
  const [page, setPage] = useState(0)
  const [confirming, setConfirming] = useState<string | null>(null)
  const create = useMutation(useServiceRetirementsCreate)
  const request = useDeletionRequest(project, onChanged)
  const preview = useQuery(
    useServiceBackupsPreviewRetention,
    {
      path: { id: anchor },
      query: { older_than_days: settings.days, keep_last: settings.keep },
    },
    { staleTime: 0 }
  )
  useRecoveryPolling(false, preview.refetch)
  const valid =
    Number.isInteger(Number(days)) &&
    Number(days) >= 1 &&
    Number(days) <= 3650 &&
    Number.isInteger(Number(keep)) &&
    Number(keep) >= 1 &&
    Number(keep) <= 1000
  const stale = Number(days) !== settings.days || Number(keep) !== settings.keep
  const owner = can("service_backup:retire")
  const enableBlocked =
    !valid || stale
      ? "Update the preview before enabling automatic deletion with these settings."
      : preview.status !== "success" || preview.isFetching
        ? "Load a fresh retention preview before enabling automatic deletion."
        : !preview.data?.complete
          ? "The history is incomplete. Automatic deletion cannot be enabled."
          : !preview.data.points.find((point) => point.backupId === anchor)
                ?.deletionSupported
            ? "The captured storage cannot safely delete this history's backup files."
            : !preview.data.verifiedBackupId
              ? "Test a restore from this history before enabling automatic deletion."
              : null
  const currentPage = Math.min(
    page,
    Math.max(0, Math.ceil((preview.data?.points.length ?? 0) / 50) - 1)
  )
  async function remove() {
    if (!confirming || !owner) return
    const result = await request.run(
      `delete:${confirming}:${settings.days}:${settings.keep}`,
      (requestId) =>
        create.mutateAsync({
          path: { id: confirming },
          body: {
            requestId,
            policy: { olderThanDays: settings.days, keepLast: settings.keep },
          },
        })
    )
    if (result) {
      setConfirming(null)
      onAccepted(result.id)
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(value) => {
        if (!value && !request.busy) onClose()
      }}
    >
      <DialogContent className="sm:max-w-3xl" showCloseButton={!request.busy}>
        <DialogHeader>
          <DialogTitle>Backup retention</DialogTitle>
          <DialogDescription>
            Review one database history before selecting a backup to delete.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            if (!valid || request.busy) return
            if (stale) {
              setPage(0)
              setSettings({ days: Number(days), keep: Number(keep) })
            } else void preview.refetch().catch(() => undefined)
          }}
        >
          <div className="grid min-w-0 gap-1.5">
            <span className="text-[12.5px] font-medium">
              History containing
            </span>
            <Select
              value={anchor}
              onValueChange={(id) => {
                if (id) {
                  setAnchor(id)
                  setPage(0)
                }
              }}
              disabled={request.busy}
            >
              <SelectTrigger
                aria-label="Recovery point history"
                className="w-48"
              >
                <SelectValue>
                  {formatDate(
                    points.find((point) => point.id === anchor)?.createdAt ?? ""
                  )}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {points.map((point) => (
                  <SelectItem key={point.id} value={point.id}>
                    {formatDate(point.createdAt)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <label className="grid gap-1.5 text-[12.5px] font-medium">
            Older than (days)
            <Input
              className="w-28"
              type="number"
              min={1}
              max={3650}
              step={1}
              value={days}
              disabled={request.busy}
              onChange={(e) => setDays(e.target.value)}
            />
          </label>
          <label className="grid gap-1.5 text-[12.5px] font-medium">
            Keep newest
            <Input
              className="w-24"
              type="number"
              min={1}
              max={1000}
              step={1}
              value={keep}
              disabled={request.busy}
              onChange={(e) => setKeep(e.target.value)}
            />
          </label>
          <Button
            size="sm"
            variant="outline"
            type="submit"
            disabled={!valid || request.busy}
          >
            Update preview
          </Button>
        </form>
        <RetentionPolicyControls
          key={anchor}
          project={project}
          anchor={anchor}
          settings={{ olderThanDays: settings.days, keepLast: settings.keep }}
          enableBlocked={enableBlocked}
          onChanged={onChanged}
          onOpenRetirement={onAccepted}
        />
        {stale && (
          <p role="status" className="text-[13px] text-muted-foreground">
            Update the preview before deleting with these settings.
          </p>
        )}
        {preview.status === "pending" ? (
          <p role="status">Loading retention preview…</p>
        ) : preview.status === "error" ? (
          <QueryError
            error={preview.error}
            fallback="Could not load retention preview"
            retry={preview.refetch}
          />
        ) : (
          <>
            {!preview.data?.complete && (
              <Alert variant="destructive">
                <AlertDescription>
                  The history is incomplete. Deletion is unavailable.
                </AlertDescription>
              </Alert>
            )}
            <p className="text-[13px] text-muted-foreground">
              {preview.data?.eligibleCount ?? 0} backups meet the age and count
              settings. A restore-tested point and existing recovery copies stay
              protected.
              {!owner && " Only an organization owner can request deletion."}
            </p>
            {!preview.data?.points.length ? (
              <RetirementEmpty
                title="No recovery points in this history"
                description="Create a backup and test a restore before reviewing deletion."
              />
            ) : (
              <div className="min-w-0 overflow-x-auto rounded-xl bg-card ring-1 ring-border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Backup</TableHead>
                      <TableHead>Decision</TableHead>
                      <TableHead className="text-right">Action</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {preview.data.points
                      .slice(currentPage * 50, (currentPage + 1) * 50)
                      .map((point) => {
                        const backup = points.find(
                          (p) => p.id === point.backupId
                        )
                        return (
                          <TableRow key={point.backupId}>
                            <TableCell className="whitespace-normal">
                              <span>
                                {point.createdAt
                                  ? formatDate(point.createdAt)
                                  : backup
                                    ? formatDate(backup.createdAt)
                                    : "Recovery point"}
                              </span>
                              <div className="text-xs text-muted-foreground">
                                {point.bytes.toLocaleString()} bytes
                              </div>
                            </TableCell>
                            <TableCell className="max-w-xs text-xs whitespace-normal">
                              {retentionDecision(point.reason)}
                              {point.eligible && !point.deletionSupported && (
                                <p className="mt-1 text-muted-foreground">
                                  {point.deletionUnavailableReason ||
                                    "Storage deletion is unavailable."}
                                </p>
                              )}
                            </TableCell>
                            <TableCell className="text-right">
                              {point.eligible &&
                                point.deletionSupported &&
                                preview.data?.complete &&
                                owner && (
                                  <Button
                                    size="sm"
                                    variant="destructive"
                                    disabled={
                                      request.busy ||
                                      stale ||
                                      preview.isFetching
                                    }
                                    onClick={() =>
                                      setConfirming(point.backupId)
                                    }
                                  >
                                    Delete backup
                                  </Button>
                                )}
                            </TableCell>
                          </TableRow>
                        )
                      })}
                  </TableBody>
                </Table>
              </div>
            )}
          </>
        )}
        {(preview.data?.points.length ?? 0) > 50 && (
          <div className="flex items-center justify-between gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={currentPage === 0 || request.busy}
              onClick={() => setPage(currentPage - 1)}
            >
              Newer backups
            </Button>
            <span className="text-xs text-muted-foreground">
              Page {currentPage + 1} of{" "}
              {Math.ceil((preview.data?.points.length ?? 0) / 50)}
            </span>
            <Button
              size="sm"
              variant="outline"
              disabled={
                (currentPage + 1) * 50 >= (preview.data?.points.length ?? 0) ||
                request.busy
              }
              onClick={() => setPage(currentPage + 1)}
            >
              Older backups
            </Button>
          </div>
        )}
        <AlertDialog
          open={!!confirming}
          onOpenChange={(value) => {
            if (!value && !request.busy) setConfirming(null)
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete this backup file?</AlertDialogTitle>
              <AlertDialogDescription>
                The selected file will be permanently deleted. A tested recovery
                point will be retained. The service and its data volume stay in
                place. New restores from this backup are blocked once the
                request is accepted.
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
                disabled={request.busy || !owner}
                onClick={(event) => {
                  event.preventDefault()
                  void remove()
                }}
              >
                {request.busy ? "Requesting deletion…" : "Delete backup"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </DialogContent>
    </Dialog>
  )
}
