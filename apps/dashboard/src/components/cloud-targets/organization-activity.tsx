import { CloudLoading } from "@/components/cloud-targets/infrastructure-ui"
import { useEffect, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { Sync, EnvironmentChange } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import {
  CloudEmpty,
  CloudError,
  Rows,
  Row,
  rowClass,
  useInfrastructureRefresh,
} from "./infrastructure-ui"

export function EnvironmentProgress({
  environment,
}: {
  environment: EnvironmentChange
}) {
  return (
    <div className="grid gap-1 text-xs">
      <p>
        {environment.projectId && `${environment.projectId} / `}
        {environment.environment}: {environment.status}
      </p>
      {environment.compensationMode && (
        <p className="font-medium">
          Recovery:{" "}
          {{
            restore_retained: "Restore exact retained runtime",
            recreate: "Recreate from saved inputs",
            mixed: "Restore retained runtimes and recreate managed services",
          }[environment.compensationMode] ?? environment.compensationMode}
        </p>
      )}
      {environment.compensationMessage && (
        <p>{environment.compensationMessage}</p>
      )}
      {environment.routingStatus && <p>Routing: {environment.routingStatus}</p>}
      {environment.routes?.map((route) => (
        <div key={route.routeId} className="grid gap-1 rounded-md bg-muted p-2">
          <p className="break-all">
            {route.hostname || route.routeId}: {route.status}
          </p>
          {route.message && <p>{route.message}</p>}
          {route.recordType && (
            <p className="break-all">
              Publish {route.recordType} {route.hostname} = {route.value} · TTL{" "}
              {route.ttl}s
            </p>
          )}
          {route.notBefore && !route.notBefore.startsWith("0001") && (
            <p>Previous route retained until {route.notBefore}</p>
          )}
        </div>
      ))}
      {environment.error && <CloudError message={environment.error} />}
    </div>
  )
}
export function OrganizationActivity({ cloudId }: { cloudId: string }) {
  const [before, setBefore] = useState("")
  const syncs = useQuery(api.useOrganizationCloudSyncsList, {
    query: { limit: 100, ...(before ? { before } : {}) },
  })
  const { refetch: refetchSyncs } = syncs
  useEffect(() => {
    void refetchSyncs().catch(() => undefined)
  }, [refetchSyncs])
  const retry = useMutation(api.useOrganizationCloudSyncsRetry)
  const [error, setError] = useState("")
  const list =
    syncs.data?.syncs.filter((s): s is Sync => !!s && s.cloudId === cloudId) ??
    []
  useInfrastructureRefresh(
    list.some((s) =>
      [
        "pending",
        "running",
        "reverting",
        "routing_pending",
        "cleanup_pending",
      ].includes(s.status)
    ),
    syncs.refetch
  )
  async function again(sync: Sync) {
    setError("")
    try {
      await retry.mutateAsync({ path: { syncId: sync.id } })
      await syncs.refetch()
    } catch (err) {
      setError(
        messageOf(
          err,
          "The sync couldn't be retried. Reconcile the original attempt or review recovery."
        )
      )
    }
  }
  return (
    <section aria-label="Cloud sync activity" className="grid gap-3">
      <h2 className="text-[15px] font-semibold">
        Desired and applied placement
      </h2>
      <p className="text-xs text-muted-foreground">
        Accepted target changes set the desired destination. Each environment
        keeps serving its applied release until deployment and routing succeed.
      </p>
      <CloudError message={error} />
      {syncs.status === "error" ? (
        <QueryError
          error={syncs.error}
          fallback="Cloud activity couldn't be loaded."
          retry={syncs.refetch}
        />
      ) : !syncs.data ? (
        <CloudLoading>Loading Cloud activity...</CloudLoading>
      ) : !list.length ? (
        <CloudEmpty title="No Cloud syncs">
          Reviewed changes appear here with the actual deployment, route and
          cleanup progress.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((sync) => (
            <Row key={sync.id} className="grid gap-2 p-3">
              <div className={rowClass}>
                <p className="text-sm font-medium">
                  {sync.status} · attempt {sync.attempt}
                </p>
                {["failed", "cleanup_pending", "routing_pending"].includes(
                  sync.status
                ) && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={retry.isPending}
                    onClick={() => void again(sync)}
                  >
                    Retry sync
                  </Button>
                )}
              </div>
              <p className="text-xs break-words">
                Desired:{" "}
                {sync.default
                  ? sync.afterDefault
                    ? `${sync.afterDefault.provider} / ${sync.afterDefault.connectionId}`
                    : "default cleared"
                  : `${sync.after?.runtime || sync.after?.provider || "saved placement"} / ${sync.after?.poolId || sync.after?.connectionId || ""}`}
                . Reviewed previous destination:{" "}
                {sync.default
                  ? sync.beforeDefault
                    ? `${sync.beforeDefault.provider} / ${sync.beforeDefault.connectionId}`
                    : "no default"
                  : `${sync.before?.runtime || sync.before?.provider || ""} / ${sync.before?.poolId || sync.before?.connectionId || ""}`}
              </p>
              {sync.environments.map((env) => (
                <EnvironmentProgress
                  key={`${env.projectId}:${env.environmentId}`}
                  environment={env}
                />
              ))}
              {sync.error && <CloudError message={sync.error} />}
            </Row>
          ))}
        </Rows>
      )}
      {syncs.data?.next && (
        <Button
          variant="outline"
          size="sm"
          onClick={() => setBefore(syncs.data?.next ?? "")}
        >
          Older syncs
        </Button>
      )}
      {before && (
        <Button variant="ghost" onClick={() => setBefore("")}>
          Latest syncs
        </Button>
      )}
    </section>
  )
}
