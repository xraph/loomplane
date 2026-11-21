import { CloudLoading } from "./infrastructure-ui"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { useEffect, useRef, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  ChangeInputBody,
  Preview,
  Target,
} from "@/client/forge/loomplane/types"
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
import { EnvironmentProgress } from "./organization-activity"
import { useOrganization } from "@/lib/organization"

/**
 * DefaultCloudIntent makes cloudId the project's default Cloud, or with ""
 * leaves the project without one. revision is the current default's.
 */
type DefaultCloudIntent = {
  cloudId: string
  revision: number
}
type ReviewProps = {
  /** The project, or "" for a Cloud the organization owns. */
  projectId: string
  title: string
  change?: ChangeInputBody
  defaultCloud?: DefaultCloudIntent
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
        props.defaultCloud,
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
  defaultCloud,
  revert,
  onClose,
  onApplied,
}: ReviewProps) {
  const accounts = useQuery(api.useConnectionsList)
  const accountName = (id: string) =>
    accounts.data?.connections.find((c) => c.id === id)?.name ?? id
  const previewChange = useMutation(api.useCloudChangesPreview)
  const previewDefaultCloud = useMutation(api.useDefaultCloudPreview)
  const previewRevert = useMutation(api.useCloudSyncsPreviewRevert)
  const apply = useMutation(api.useCloudChangesApply)
  const previewOrgChange = useMutation(api.useOrganizationCloudChangesPreview)
  const applyOrg = useMutation(api.useOrganizationCloudChangesApply)
  // An organization's Cloud reviews target changes at the organization.
  const org = projectId === ""
  const applyRevert = useMutation(api.useCloudSyncsApplyRevert)
  const { can } = useOrganization()
  const [preview, setPreview] = useState<Preview | null>(null)
  const [error, setError] = useState("")
  const [stale, setStale] = useState(false)
  const [loading, setLoading] = useState(true)
  const [version, setVersion] = useState(0)
  const [latestChange, setLatestChange] = useState(change)
  const [latestDefault, setLatestDefault] = useState(defaultCloud)
  const idempotency = useRef(crypto.randomUUID())
  const intent = JSON.stringify({
    projectId,
    latestChange,
    latestDefault,
    revert,
  })
  useEffect(() => {
    let current = true
    const load = revert
      ? previewRevert.mutateAsync({ path: { id: projectId, syncId: revert } })
      : latestDefault
        ? previewDefaultCloud.mutateAsync({
            path: { id: projectId },
            body: {
              cloudId: latestDefault.cloudId,
              revision: latestDefault.revision,
            },
          })
        : org
          ? previewOrgChange.mutateAsync({ body: latestChange! })
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
        const current = await (
          org
            ? api.useOrganizationCloudTargetsGet({
                path: { cloudId: change.cloudId, targetId: change.targetId },
              })
            : api.useCloudTargetsGet({
                path: {
                  id: projectId,
                  cloudId: change.cloudId,
                  targetId: change.targetId,
                },
              })
        ).refetch()
        setLatestChange({ ...change, revision: current.revision })
      } else if (change?.default) {
        const current = await (
          org
            ? api.useOrganizationCloudsGet({
                path: { cloudId: change.cloudId },
              })
            : api.useCloudsGet({
                path: { id: projectId, cloudId: change.cloudId },
              })
        ).refetch()
        setLatestChange({ ...change, revision: current.revision })
      } else if (defaultCloud) {
        const current = await api
          .useDefaultCloudGet({ path: { id: projectId } })
          .refetch()
        setLatestDefault({ ...defaultCloud, revision: current.revision })
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
  const busy = apply.isPending || applyRevert.isPending || applyOrg.isPending
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
      else if (org) await applyOrg.mutateAsync({ body: args.body })
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
        {loading && <CloudLoading>Building preview...</CloudLoading>}
        {preview && (
          <div className="grid gap-3 text-[13px]">
            {(preview.beforeDefault ||
              preview.afterDefault ||
              change?.default) && (
              <div className="grid gap-1 text-[13px]">
                <p>
                  Current default:{" "}
                  {preview.beforeDefault
                    ? `${preview.beforeDefault.provider} / ${accountName(preview.beforeDefault.connectionId)}`
                    : "none"}
                </p>
                <p>
                  Desired default:{" "}
                  {preview.afterDefault
                    ? `${preview.afterDefault.provider} / ${accountName(preview.afterDefault.connectionId)}`
                    : "cleared"}
                </p>
              </div>
            )}
            {preview.before && (
              <p className="break-words">
                Current: {preview.before.runtime || preview.before.provider} /{" "}
                {preview.before.poolId}{" "}
                {accountName(preview.before.connectionId)}
              </p>
            )}
            {preview.after && (
              <p className="break-words">
                Desired: {preview.after.runtime || preview.after.provider} /{" "}
                {preview.after.poolId} {accountName(preview.after.connectionId)}
                {preview.after.region ? ` / ${preview.after.region}` : ""}
              </p>
            )}
            {(defaultCloud || preview.beforeCloud || preview.afterCloud) && (
              <DefaultCloudPreview
                preview={preview}
                accountName={accountName}
              />
            )}
            {(preview.blockers ?? []).map((blocker, i) => (
              <Alert variant="destructive" key={i} className="text-destructive">
                <AlertDescription>{blocker}</AlertDescription>
              </Alert>
            ))}
            {(preview.environments ?? []).map((env) => (
              <div key={env.environmentId} className="rounded-lg border p-3">
                <EnvironmentProgress environment={env} heading />
                <p className="text-muted-foreground">
                  {env.services?.join(", ") ||
                    (preview.blockers?.length || env.error
                      ? "Service details are unavailable while this preview is blocked."
                      : "No deployed services")}
                </p>
                {!preview.blockers?.length && !env.error && (
                  <p className="text-xs text-muted-foreground">
                    {env.summary.replace} replacements · {env.summary.update}{" "}
                    updates · {env.summary.adopt} adoptions · {env.summary.keep}{" "}
                    unchanged
                  </p>
                )}
                {env.externalRouting && (
                  <p className="text-xs text-weld-ink">
                    External routing must be confirmed before cleanup.
                  </p>
                )}
              </div>
            ))}
            {!preview.environments?.length && (
              <p className="text-muted-foreground">
                {preview.blockers?.length
                  ? "Resolve the blockers above before reviewing deployment work."
                  : "No running services need a deployment. Save this placement for the next release."}
              </p>
            )}
          </div>
        )}
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        {stale && (
          <Alert variant="destructive">
            <AlertDescription>
              This review is out of date. Refresh it before applying.
            </AlertDescription>
          </Alert>
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
                !can(
                  org ? "organization_cloud:manage" : "cloud_account:manage"
                ) ||
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

/**
 * DefaultCloudPreview shows a project default change: which Cloud every
 * environment resolves through before and after, and what each target
 * name a service may use points at on either side.
 */
function DefaultCloudPreview({
  preview,
  accountName,
}: {
  preview: Preview
  accountName: (id: string) => string
}) {
  const clouds = useQuery(api.useCloudsList, {
    path: { id: preview.projectId },
  })
  const name = (id: string) =>
    clouds.data?.clouds?.find((cloud) => cloud?.id === id)?.name ?? id
  const before = preview.beforeCloud
  const after = preview.afterCloud
  const where = (t: Target) =>
    `${t.provider} / ${accountName(t.connectionId)}${t.region ? ` / ${t.region}` : ""}`
  const targetNames = [
    ...new Set([
      ...(before?.targets ?? []).map((t) => t.name),
      ...(after?.targets ?? []).map((t) => t.name),
    ]),
  ].sort()
  return (
    <section className="grid gap-2" aria-label="Default Cloud change">
      <p className="break-words">
        Every environment moves from{" "}
        <span className="font-medium">
          {before ? name(before.cloudId) : "no default Cloud"}
        </span>{" "}
        to{" "}
        <span className="font-medium">
          {after
            ? name(after.cloudId)
            : preview.input.cloudId
              ? name(preview.input.cloudId)
              : "no default Cloud"}
        </span>
        .
      </p>
      {targetNames.length > 0 && (
        <div className="divide-y rounded-lg border text-xs">
          {targetNames.map((slug) => {
            const current = before?.targets.find((t) => t.name === slug)
            const desired = after?.targets.find((t) => t.name === slug)
            return (
              <div
                key={slug}
                className="grid gap-0.5 px-3 py-2 sm:grid-cols-[8rem_1fr]"
              >
                <span className="font-mono font-medium">{slug}</span>
                <span className="break-words text-muted-foreground">
                  {current ? where(current) : "Not in the current Cloud"}
                  {" → "}
                  {desired
                    ? where(desired)
                    : "Not in the new Cloud: services that name it stop planning"}
                </span>
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
