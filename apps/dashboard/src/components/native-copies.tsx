import { useState } from "react"
import { useMutation, useQuery } from "@/client/hooks"
import {
  useNativeRecoveryCopiesList,
  useNativeRecoveryCopiesCleanup,
} from "@/client/forge/loomplane/hooks"
import type { NativeRestore } from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { useOrganization } from "@/lib/organization"
import { useNow } from "@/lib/use-runs"
import { formatDate } from "@/lib/format"
import { NativeEmpty } from "@/components/native-recovery-empty"
import { NativeCopyCredentialsControl } from "@/components/native-copy-credentials"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog"

function copyStatus(copy: NativeRestore) {
  if (copy.status === "succeeded")
    return copy.verifiedAt ? "SQL verification recorded" : "SQL proof missing"
  return (
    (
      {
        queued: "Copy queued",
        running: "Creating and verifying",
        failed: "Copy failed",
        deleting: "Cleanup pending",
        cleanup_failed: "Cleanup needs attention",
        deleted: "Copy deleted",
      } as Record<string, string>
    )[copy.status] ?? copy.status
  )
}

export function NativeCopyHistory({
  project,
  environment,
  resource,
}: {
  project: string
  environment?: string
  resource?: string
}) {
  const { can } = useOrganization()
  const [pages, setPages] = useState<string[]>([""])
  const [confirming, setConfirming] = useState<NativeRestore | null>(null)
  const [failure, setFailure] = useState("")
  const copies = useQuery(useNativeRecoveryCopiesList, {
    path: { id: project },
    query: {
      environment_id: environment,
      resource: resource || undefined,
      before: pages.at(-1) || undefined,
      limit: 25,
    },
  })
  const cleanup = useMutation(useNativeRecoveryCopiesCleanup)
  const now = useNow()
  const list = copies.data?.copies ?? []
  useRecoveryPolling(
    list.some((copy) =>
      ["queued", "running", "deleting"].includes(copy.status)
    ),
    copies.refetch
  )
  async function remove() {
    if (!confirming || cleanup.isPending || !can("service_restore:manage"))
      return
    setFailure("")
    try {
      await cleanup.mutateAsync({ path: { id: confirming.id } })
      setConfirming(null)
      await copies.refetch()
    } catch (cause) {
      setFailure(
        messageOf(
          cause,
          "Cleanup was not confirmed. Check the copy status and retry."
        )
      )
    }
  }
  return (
    <section aria-label="Native recovery copies" className="grid min-w-0 gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-sm font-medium">Native recovery copies</h4>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void copies.refetch().catch(() => undefined)}
        >
          Reload copies
        </Button>
      </div>
      {copies.status === "error" && (
        <QueryError
          error={copies.error}
          fallback="Could not load native copies"
          retry={copies.refetch}
        />
      )}
      {!copies.data && copies.status === "pending" ? (
        <p role="status">Loading native copies…</p>
      ) : list.length ? (
        <ul aria-label="Temporary native copies" className="divide-y">
          {list.map((copy) => (
            <li key={copy.id} className="grid min-w-0 gap-1 py-2 text-xs">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="min-w-0 break-all">
                  <span className="font-medium">{copyStatus(copy)}</span> ·{" "}
                  {copy.id}
                </p>
                {can("service_backup:read") &&
                  can("service_restore:manage") &&
                  can("secret:manage") &&
                  copies.status === "success" &&
                  copy.status === "succeeded" &&
                  copy.verifiedAt &&
                  !copy.cleanupRequestedAt &&
                  new Date(copy.expiresAt).getTime() > now && (
                    <NativeCopyCredentialsControl copy={copy} />
                  )}
                {can("service_restore:manage") &&
                  copies.status === "success" &&
                  !["deleted", "deleting"].includes(copy.status) && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={cleanup.isPending}
                      onClick={() => {
                        setFailure("")
                        setConfirming(copy)
                      }}
                    >
                      {copy.status === "cleanup_failed"
                        ? "Retry cleanup"
                        : "Clean up copy"}
                    </Button>
                  )}
              </div>
              <p className="break-all text-muted-foreground">
                {copy.target.nativeProject ||
                  copy.target.nativeAccount ||
                  copy.target.provider}{" "}
                · {copy.target.region} · Source {copy.sourceId}
              </p>
              {copy.restoreTime && (
                <p className="break-all text-muted-foreground">
                  PITR time {copy.restoreTime}
                </p>
              )}
              <p className="text-muted-foreground">
                {copy.verifiedAt
                  ? `SQL verified ${formatDate(copy.verifiedAt)}`
                  : "Not SQL verified"}{" "}
                ·{" "}
                {new Date(copy.expiresAt).getTime() <= now
                  ? "Expired"
                  : "Expires"}{" "}
                {formatDate(copy.expiresAt)}
              </p>
              {copy.status !== "deleted" && (
                <p className="text-muted-foreground">
                  Charges continue until deletion is confirmed.
                </p>
              )}
              {copy.error && (
                <p className="break-words text-destructive">{copy.error}</p>
              )}
            </li>
          ))}
        </ul>
      ) : (
        copies.status !== "error" && (
          <NativeEmpty title="No native recovery copies">
            Choose a Cloud SQL PostgreSQL 17 snapshot below to review a
            temporary copy and its charges. Snapshot observations alone do not
            establish a successful restore.
          </NativeEmpty>
        )
      )}
      {(pages.length > 1 || copies.data?.nextBefore) && (
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={pages.length === 1 || copies.isFetching}
            onClick={() => setPages((current) => current.slice(0, -1))}
          >
            Previous copies
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={
              !copies.data?.nextBefore ||
              copies.status !== "success" ||
              copies.isFetching
            }
            onClick={() =>
              setPages((current) => [...current, copies.data!.nextBefore!])
            }
          >
            More copies
          </Button>
        </div>
      )}
      <AlertDialog
        open={!!confirming}
        onOpenChange={(open) => {
          if (!open && !cleanup.isPending) {
            setConfirming(null)
            setFailure("")
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clean up temporary copy?</AlertDialogTitle>
            <AlertDialogDescription className="break-words">
              Remove {confirming?.id} from{" "}
              {confirming?.target.nativeProject ||
                confirming?.target.nativeAccount ||
                confirming?.target.provider}
              . This removes the temporary copy and its credentials. The source
              database stays unchanged. Charges continue if cleanup fails.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {failure && (
            <Alert variant="destructive">
              <AlertDescription>{failure}</AlertDescription>
            </Alert>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={cleanup.isPending}>
              Cancel
            </AlertDialogCancel>
            <Button
              size="sm"
              variant="destructive"
              disabled={cleanup.isPending || !can("service_restore:manage")}
              onClick={() => void remove()}
            >
              {cleanup.isPending ? "Requesting cleanup…" : "Confirm cleanup"}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
