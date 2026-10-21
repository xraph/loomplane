import { useEffect, useRef, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { ChangeInputBody, Preview } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useOrganization } from "@/lib/organization"

type AssignmentIntent = {
  environmentId: string
  cloudId: string
  revision: number
}
type ReviewProps = {
  projectId: string
  title: string
  change?: ChangeInputBody
  assignment?: AssignmentIntent
  revert?: string
  onClose: () => void
  onApplied: () => void
}
export function ChangeReview(props: ReviewProps) {
  return (
    <ReviewContent
      key={JSON.stringify([
        props.projectId,
        props.change,
        props.assignment,
        props.revert,
      ])}
      {...props}
    />
  )
}
function ReviewContent({
  projectId,
  title,
  change,
  assignment,
  revert,
  onClose,
  onApplied,
}: ReviewProps) {
  const accounts = useQuery(api.useConnectionsList)
  const accountName = (id: string) =>
    accounts.data?.connections.find((c) => c.id === id)?.name ?? id
  const previewChange = useMutation(api.useCloudChangesPreview)
  const previewAssignment = useMutation(api.useEnvironmentCloudsPreview)
  const previewRevert = useMutation(api.useCloudSyncsPreviewRevert)
  const apply = useMutation(api.useCloudChangesApply)
  const applyRevert = useMutation(api.useCloudSyncsApplyRevert)
  const { can } = useOrganization()
  const [preview, setPreview] = useState<Preview | null>(null)
  const [error, setError] = useState("")
  const [stale, setStale] = useState(false)
  const [loading, setLoading] = useState(true)
  const [version, setVersion] = useState(0)
  const [latestChange, setLatestChange] = useState(change)
  const [latestAssignment, setLatestAssignment] = useState(assignment)
  const idempotency = useRef(crypto.randomUUID())
  const intent = JSON.stringify({
    projectId,
    latestChange,
    latestAssignment,
    revert,
  })
  useEffect(() => {
    let current = true
    const load = revert
      ? previewRevert.mutateAsync({ path: { id: projectId, syncId: revert } })
      : latestAssignment
        ? previewAssignment.mutateAsync({
            path: {
              id: projectId,
              environmentId: latestAssignment!.environmentId,
            },
            body: {
              cloudId: latestAssignment!.cloudId,
              revision: latestAssignment!.revision,
            },
          })
        : previewChange.mutateAsync({
            path: { id: projectId },
            body: latestChange!,
          })
    void load
      .then((value) => {
        if (current) setPreview(value)
      })
      .catch((err) => {
        if (current)
          setError(messageOf(err, "This change couldn't be previewed."))
      })
      .finally(() => {
        if (current) setLoading(false)
      })
    return () => {
      current = false
    }
    // The serialized intent is the complete immutable review scope.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent, version])
  const refreshReview = async () => {
    setPreview(null)
    setError("")
    setStale(false)
    setLoading(true)
    idempotency.current = crypto.randomUUID()
    try {
      if (change?.targetId) {
        const current = await api
          .useCloudTargetsGet({
            path: {
              id: projectId,
              cloudId: change.cloudId,
              targetId: change.targetId,
            },
          })
          .refetch()
        setLatestChange({ ...change, revision: current.revision })
      } else if (assignment) {
        const current = await api
          .useEnvironmentCloudsGet({
            path: { id: projectId, environmentId: assignment.environmentId },
          })
          .refetch()
        setLatestAssignment({ ...assignment, revision: current.revision })
      }
      setVersion((v) => v + 1)
    } catch (err) {
      setError(
        messageOf(err, "The current configuration couldn't be reloaded.")
      )
      setLoading(false)
      setStale(true)
    }
  }
  const busy = apply.isPending || applyRevert.isPending
  const submit = async () => {
    if (!preview || busy || stale) return
    setError("")
    const args = {
      path: { id: projectId },
      body: {
        previewId: preview.id,
        revision: preview.revision,
        idempotencyKey: idempotency.current,
      },
    }
    try {
      if (revert) await applyRevert.mutateAsync(args)
      else await apply.mutateAsync(args)
      onApplied()
    } catch (err) {
      setError(
        messageOf(
          err,
          "The change wasn't accepted. You can retry this same review."
        )
      )
      if ([409, 422].includes(statusOf(err) ?? 0)) setStale(true)
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            Review every affected environment before saving the desired
            placement and syncing its applied release.
          </DialogDescription>
        </DialogHeader>
        {loading && (
          <p role="status" className="text-sm">
            Building preview...
          </p>
        )}
        {preview && (
          <div className="grid gap-3 text-sm">
            {preview.before && (
              <p className="break-words">
                Current: {preview.before.provider} /{" "}
                {accountName(preview.before.connectionId)}
              </p>
            )}
            {preview.after && (
              <p className="break-words">
                Desired: {preview.after.provider} /{" "}
                {accountName(preview.after.connectionId)}
                {preview.after.region ? ` / ${preview.after.region}` : ""}
              </p>
            )}
            {assignment && (
              <p>
                {assignment.cloudId
                  ? "Assign the selected Cloud"
                  : "Remove this environment's Cloud assignment"}
              </p>
            )}
            {(preview.blockers ?? []).map((blocker, i) => (
              <p role="alert" key={i} className="text-destructive">
                {blocker}
              </p>
            ))}
            {(preview.environments ?? []).map((env) => (
              <div key={env.environmentId} className="rounded-lg border p-3">
                <p className="font-medium">
                  {env.environment}: {env.status}
                </p>
                <p className="text-muted-foreground">
                  {env.services?.join(", ") || "No deployed services"}
                </p>
                <p className="text-xs text-muted-foreground">
                  {env.summary.replace} replacements · {env.summary.update}{" "}
                  updates · {env.summary.adopt} adoptions · {env.summary.keep}{" "}
                  unchanged
                </p>
                {env.externalRouting && (
                  <p className="text-weld-ink text-xs">
                    External routing must be confirmed before cleanup.
                  </p>
                )}
                {env.error && (
                  <p role="alert" className="text-destructive">
                    {env.error}
                  </p>
                )}
              </div>
            ))}
            {!preview.environments?.length && (
              <p className="text-muted-foreground">
                No running services need a deployment. Save this placement for
                the next release.
              </p>
            )}
          </div>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        {stale && (
          <p role="alert" className="text-sm">
            This review is out of date. Refresh it before applying.
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          {(!preview || stale) && !loading ? (
            <Button disabled={busy} onClick={() => void refreshReview()}>
              Refresh preview
            </Button>
          ) : (
            <Button
              disabled={
                loading ||
                busy ||
                !preview ||
                !!preview.blockers?.length ||
                !can("cloud_account:manage") ||
                !can("deployment:approve")
              }
              onClick={() => void submit()}
            >
              {busy
                ? "Accepting..."
                : revert
                  ? "Revert and sync"
                  : "Switch and sync"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
