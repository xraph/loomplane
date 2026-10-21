import { useEffect, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { Sync } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { QueryError } from "@/components/query-error"
import { useOrganization } from "@/lib/organization"
import { activeSync, syncLabel } from "@/lib/cloud-targets"
import { ChangeReview } from "./change-review"
import { EmptyClouds } from "./workspace"

export function SyncHistory({
  projectId,
  environmentId,
  refresh,
  onChanged,
}: {
  projectId: string
  environmentId: string
  refresh: number
  onChanged: () => Promise<void>
}) {
  const { can } = useOrganization()
  return can("deployment:read") ? (
    <SyncList
      projectId={projectId}
      environmentId={environmentId}
      refresh={refresh}
      onChanged={onChanged}
    />
  ) : (
    <EmptyClouds
      title="Sync history is restricted"
      description="You need deployment read permission to view applied results."
    />
  )
}
function SyncList({
  projectId,
  environmentId,
  refresh,
  onChanged,
}: {
  projectId: string
  environmentId: string
  refresh: number
  onChanged: () => Promise<void>
}) {
  const [before, setBefore] = useState("")
  const syncs = useQuery(api.useCloudSyncsList, {
    path: { id: projectId },
    query: { limit: 100, ...(before ? { before } : {}) },
  })
  const { refetch: refetchSyncs } = syncs
  const [revert, setRevert] = useState<string | null>(null)
  const reload = async () => {
    await refetchSyncs()
    await onChanged()
  }
  useEffect(() => {
    void refetchSyncs().catch(() => undefined)
  }, [refresh, refetchSyncs])
  const all = syncs.data?.syncs ?? []
  const rows = all.filter(
    (s): s is Sync =>
      !!s &&
      (s.environments ?? []).some((e) => e.environmentId === environmentId)
  )
  const moving = all.some((s) => s && activeSync(s))
  useEffect(() => {
    const timer = window.setInterval(
      () => {
        void refetchSyncs().catch(() => undefined)
      },
      moving ? 3000 : 15000
    )
    return () => window.clearInterval(timer)
  }, [moving, refetchSyncs])
  return (
    <section className="grid gap-3">
      <h2 className="font-semibold">Sync history</h2>
      {syncs.status === "error" ? (
        <QueryError
          error={syncs.error}
          fallback="Sync history couldn't be loaded."
          retry={reload}
        />
      ) : !syncs.data ? (
        <p role="status">Loading sync history...</p>
      ) : !rows.length ? (
        <EmptyClouds
          title="No syncs in this environment"
          description="Reviewed Cloud changes appear here with each environment's persisted result."
        />
      ) : (
        <div className="divide-y rounded-xl border">
          {rows.map((sync) => (
            <SyncRow
              key={sync.id}
              projectId={projectId}
              sync={sync}
              onChanged={reload}
              onRevert={() => setRevert(sync.id)}
            />
          ))}
        </div>
      )}
      {syncs.data?.next && (
        <Button
          size="sm"
          variant="outline"
          onClick={() => setBefore(syncs.data?.next ?? "")}
        >
          Older syncs
        </Button>
      )}
      {before && (
        <Button size="sm" variant="ghost" onClick={() => setBefore("")}>
          Latest syncs
        </Button>
      )}
      {revert && (
        <ChangeReview
          projectId={projectId}
          title="Review revert"
          revert={revert}
          onClose={() => setRevert(null)}
          onApplied={() => {
            setRevert(null)
            void reload()
          }}
        />
      )}
    </section>
  )
}
function SyncRow({
  projectId,
  sync,
  onChanged,
  onRevert,
}: {
  projectId: string
  sync: Sync
  onChanged: () => Promise<void>
  onRevert: () => void
}) {
  const { can } = useOrganization()
  const permitted = can("cloud_account:manage") && can("deployment:approve")
  const accounts = useQuery(api.useConnectionsList)
  const accountName = (id?: string) =>
    accounts.data?.connections.find((c) => c.id === id)?.name ?? id
  const retry = useMutation(api.useCloudSyncsRetry)
  const routing = useMutation(api.useCloudSyncsCompleteRouting)
  const [evidence, setEvidence] = useState<Record<string, string>>({})
  const [error, setError] = useState("")
  const busy = retry.isPending || routing.isPending
  const act = async (environmentId?: string) => {
    setError("")
    try {
      if (environmentId)
        await routing.mutateAsync({
          path: { id: projectId, syncId: sync.id },
          body: { environmentId, evidence: evidence[environmentId].trim() },
        })
      else await retry.mutateAsync({ path: { id: projectId, syncId: sync.id } })
      await onChanged()
    } catch (err) {
      setError(messageOf(err, "The sync action wasn't accepted."))
    }
  }
  return (
    <article className="grid gap-2 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm font-medium">
            {syncLabel(sync.status)}{" "}
            <span className="text-xs font-normal text-muted-foreground">
              Attempt {sync.attempt}
            </span>
          </p>
          <p className="text-xs break-all text-muted-foreground">{sync.id}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {["failed", "cleanup_pending"].includes(sync.status) && (
            <Button
              size="sm"
              variant="outline"
              disabled={!permitted || busy}
              onClick={() => void act()}
            >
              Retry sync
            </Button>
          )}
          {!activeSync(sync) && sync.status !== "reverted" && (
            <Button
              size="sm"
              variant="outline"
              disabled={!permitted || busy}
              onClick={onRevert}
            >
              Review revert
            </Button>
          )}
        </div>
      </div>
      {sync.before && (
        <p className="text-xs break-words text-muted-foreground">
          Previous: {accountName(sync.before.connectionId)} · Desired:{" "}
          {accountName(sync.after?.connectionId) ?? "restored placement"}
        </p>
      )}
      {sync.error && (
        <p role="alert" className="text-sm text-destructive">
          {sync.error}
        </p>
      )}
      {(sync.environments ?? []).map((env) => (
        <div
          key={env.environmentId}
          className="grid gap-1 border-l-2 pl-3 text-xs"
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{env.environment}</span>
            <span>
              {env.status === "succeeded" &&
              env.externalRouting &&
              !env.routingEvidence
                ? "Deployment ready, routing pending"
                : syncLabel(env.status)}
            </span>
            {env.deploymentId && (
              <a
                className="underline underline-offset-4"
                href={`/deployments/${env.deploymentId}`}
              >
                Deployment and logs
              </a>
            )}
          </div>
          <p className="text-muted-foreground">
            {env.services?.join(", ") || "No service changes"}
          </p>
          {env.error && (
            <p role="alert" className="text-destructive">
              {env.error}
            </p>
          )}
          {env.externalRouting &&
            sync.status === "routing_pending" &&
            !env.routingEvidence && (
              <div className="flex flex-wrap items-center gap-2">
                <label className="grid min-w-40 flex-1 gap-1">
                  Confirm external routing
                  <Input
                    aria-label={`Routing evidence for ${env.environment}`}
                    placeholder="Route change or verification evidence"
                    value={evidence[env.environmentId] ?? ""}
                    disabled={!permitted || busy}
                    onChange={(e) =>
                      setEvidence((v) => ({
                        ...v,
                        [env.environmentId]: e.target.value,
                      }))
                    }
                  />
                </label>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={
                    !permitted || busy || !evidence[env.environmentId]?.trim()
                  }
                  onClick={() => void act(env.environmentId)}
                >
                  Confirm routing
                </Button>
              </div>
            )}
          {env.routingEvidence && (
            <p className="break-words text-muted-foreground">
              Routing evidence: {env.routingEvidence}
            </p>
          )}
        </div>
      ))}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </article>
  )
}
