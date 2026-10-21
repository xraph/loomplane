import { useCallback, useEffect, useState } from "react"
import { RotateCcwIcon } from "lucide-react"
import { useQuery } from "@/client/hooks"
import { useServiceRetirementsList } from "@/client/forge/loomplane/hooks"
import type { Backup, Retirement } from "@/client/forge/loomplane/types"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { useOrganization } from "@/lib/organization"
import { formatDate } from "@/lib/format"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { TableRow, TableCell } from "@/components/ui/table"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { RetirementDetails } from "@/components/backup-retirement/details"
import {
  RetirementEmpty,
  deletionStatus,
  deletionMessage,
} from "@/components/backup-retirement/shared"

type Props = {
  project: string
  backup: Backup
  busy: boolean
  onRestore: (id: string) => void
  onRetry: (id: string) => void
  onChanged: () => Promise<unknown>
}

export function RecoveryPointRow(props: Props) {
  const [details, setDetails] = useState<string | true | null>(null)
  const onDetails = (id?: string) => setDetails(id || true)
  return (
    <>
      {props.backup.status === "retiring" ? (
        <RetiringPoint {...props} onDetails={onDetails} />
      ) : (
        <PointView {...props} onDetails={onDetails} />
      )}
      {details &&
        (typeof details === "string" ? (
          <RetirementDetails
            project={props.project}
            id={details}
            onChanged={props.onChanged}
            onClose={() => setDetails(null)}
          />
        ) : (
          <DeletionLookup
            project={props.project}
            backup={props.backup.id}
            onChanged={props.onChanged}
            onClose={() => setDetails(null)}
          />
        ))}
    </>
  )
}

function matchingRecord(
  records: Retirement[] | undefined,
  project: string,
  backup: string
) {
  const matching =
    records?.filter(
      (record) => record.projectId === project && record.backupId === backup
    ) ?? []
  return matching.length === 1 ? matching[0] : undefined
}

function RetiringPoint(props: Props & { onDetails: (id?: string) => void }) {
  const state = useQuery(useServiceRetirementsList, {
    path: { id: props.project },
    query: { backup_id: props.backup.id, limit: 1 },
  })
  const record = matchingRecord(
    state.data?.retirements,
    props.project,
    props.backup.id
  )
  const { refetch } = state
  const refresh = useCallback(() => refetch(), [refetch])
  useRecoveryPolling(
    !!record && ["queued", "running"].includes(record.status),
    refresh
  )
  const { onChanged } = props
  const status = record?.status
  useEffect(() => {
    if (status && ["succeeded", "failed"].includes(status))
      void onChanged().catch(() => undefined)
  }, [status, onChanged])
  return (
    <PointView
      {...props}
      operation={record}
      unavailable={
        state.status === "error" || (state.status === "success" && !record)
      }
    />
  )
}

function PointView({
  backup: b,
  busy,
  onRestore,
  onRetry,
  onDetails,
  operation,
  unavailable,
}: Props & {
  operation?: Retirement
  unavailable?: boolean
  onDetails: (id?: string) => void
}) {
  const { can } = useOrganization()
  const deletion = ["retiring", "retired"].includes(b.status)
  const label =
    b.status === "retired"
      ? "Backup file deleted"
      : b.status === "retiring"
        ? deletionStatus(operation?.status ?? "")
        : b.status
  const error = unavailable
    ? "Deletion status is unavailable. Open details to refresh it."
    : deletion
      ? operation?.status === "failed" && operation.error
        ? deletionMessage(operation.error)
        : undefined
      : b.error
  const evidence = b.restoredAt
    ? `${b.status === "retired" ? "Previously verified" : "Restore verified"} ${formatDate(b.restoredAt)}`
    : "Not tested"
  return (
    <>
      <TableRow>
        <TableCell className="whitespace-normal">
          <div>{formatDate(b.createdAt)}</div>
          <div className="text-xs text-muted-foreground">
            {b.bytes.toLocaleString()} bytes
          </div>
          <div className="mt-1 grid gap-1 text-xs sm:hidden">
            <Badge
              variant={
                operation?.status === "failed" ? "destructive" : "secondary"
              }
            >
              {label}
            </Badge>
            <span>{evidence}</span>
            {error && <span className="text-destructive">{error}</span>}
          </div>
        </TableCell>
        <TableCell className="hidden sm:table-cell">
          <Badge
            variant={
              operation?.status === "failed" ? "destructive" : "secondary"
            }
          >
            {label}
          </Badge>
          {error && (
            <p className="max-w-xs text-xs text-destructive">{error}</p>
          )}
        </TableCell>
        <TableCell className="hidden sm:table-cell">{evidence}</TableCell>
        <TableCell className="text-right">
          <div className="flex flex-wrap justify-end gap-1">
            {b.status === "succeeded" && can("service_restore:manage") && (
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onRestore(b.id)}
              >
                <RotateCcwIcon />
                Test restore
              </Button>
            )}
            {b.status === "failed" && can("service_backup:manage") && (
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onRetry(b.id)}
              >
                Retry backup
              </Button>
            )}
            {deletion && (
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onDetails(operation?.id)}
              >
                Deletion details
              </Button>
            )}
          </div>
        </TableCell>
      </TableRow>
    </>
  )
}

function DeletionLookup({
  project,
  backup,
  onChanged,
  onClose,
}: {
  project: string
  backup: string
  onChanged: () => Promise<unknown>
  onClose: () => void
}) {
  const state = useQuery(useServiceRetirementsList, {
    path: { id: project },
    query: { backup_id: backup, limit: 1 },
  })
  const record = matchingRecord(state.data?.retirements, project, backup)
  if (record)
    return (
      <RetirementDetails
        project={project}
        id={record.id}
        onChanged={onChanged}
        onClose={onClose}
      />
    )
  return (
    <Dialog
      open
      onOpenChange={(value) => {
        if (!value) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Backup deletion</DialogTitle>
          <DialogDescription>
            Read the operation linked to this recovery point.
          </DialogDescription>
        </DialogHeader>
        {state.status === "pending" ? (
          <p role="status">Loading deletion record…</p>
        ) : state.status === "error" ? (
          <QueryError
            error={state.error}
            fallback="Could not read the deletion record"
            retry={state.refetch}
          />
        ) : (
          <RetirementEmpty
            title="Deletion record unavailable"
            description="Refresh the recorded state before requesting another action."
          >
            <Button
              size="sm"
              variant="outline"
              onClick={() => void state.refetch().catch(() => undefined)}
            >
              Refresh status
            </Button>
          </RetirementEmpty>
        )}
      </DialogContent>
    </Dialog>
  )
}
