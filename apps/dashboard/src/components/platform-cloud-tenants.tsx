import { useState } from "react"
import { toast } from "sonner"

import {
  usePlatformCloudTenantsList,
  usePlatformCloudTenantsRestore,
  usePlatformCloudTenantsSet,
  usePlatformCloudTenantsSuspend,
} from "@/client/forge/loomplane/hooks"
import type { TenantRow } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { LimitsDialog } from "@/components/platform-cloud-limits-dialog"
import { Confirm, errorOf } from "@/components/platform-cloud-parts"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { statusLabel, suspendMessage, usageRows } from "@/lib/platform-cloud"

const accepted = "Recorded. Running work may take a moment to stop or start."

const disabledMessage =
  "Loomplane Cloud work is stopped by the instance owner. It starts again when they enable it."

/** nameOf is a workspace's name, or its ID when it has none. */
const nameOf = (row: TenantRow) => row.orgName || row.orgId

type Asking = { kind: "disable" | "suspend"; row: TenantRow }

/** PlatformCloudTenants lists workspaces with use against limits and their controls. */
export function PlatformCloudTenants() {
  const tenants = useQuery(usePlatformCloudTenantsList)
  const set = useMutation(usePlatformCloudTenantsSet)
  const suspend = useMutation(usePlatformCloudTenantsSuspend)
  const restore = useMutation(usePlatformCloudTenantsRestore)
  const invalidate = useInvalidate()
  const [editing, setEditing] = useState<TenantRow | null>(null)
  const [error, setError] = useState("")
  const [asking, setAsking] = useState<Asking | null>(null)
  // busy holds the workspaces with an action in flight.
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())
  const mark = (org: string, on: boolean) =>
    setBusy((b) => {
      const next = new Set(b)
      if (on) next.add(org)
      else next.delete(org)
      return next
    })

  const run = async (
    org: string,
    work: () => Promise<unknown>,
    done: string,
    fail: string
  ) => {
    setError("")
    mark(org, true)
    try {
      await work()
      invalidate(usePlatformCloudTenantsList)
      toast.success(done)
    } catch (err) {
      setError(errorOf(err, fail))
    } finally {
      mark(org, false)
    }
  }

  const setStatus = (row: TenantRow, status: "enabled" | "disabled") =>
    run(
      row.orgId,
      () =>
        set.mutateAsync({
          path: { org: row.orgId },
          body: {
            status,
            ...(row.tenant?.limits ? { limits: row.tenant.limits } : {}),
          },
        }),
      status === "enabled"
        ? `Workspace enabled. ${accepted}`
        : `Workspace disabled. ${accepted}`,
      "The workspace wasn't changed."
    )

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-base font-medium">Workspaces</h2>
      {(error || tenants.status === "error") && (
        <Alert variant="destructive">
          <AlertDescription>
            {error || errorOf(tenants.error, "Workspaces couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      )}
      {!tenants.data && tenants.status !== "error" && (
        <Skeleton className="h-24 w-full rounded-xl" />
      )}
      {tenants.data && tenants.data.tenants.length === 0 && (
        <p className="text-sm text-muted-foreground">No workspaces yet.</p>
      )}
      {tenants.data && tenants.data.tenants.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Workspace</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Use</TableHead>
              <TableHead>Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tenants.data.tenants.map((row) => {
              const status = row.tenant?.status ?? "disabled"
              const reasons = row.tenant?.suspendReasons ?? []
              const enabled = status === "enabled"
              // A disabled workspace starts again only when it is enabled.
              const message =
                !enabled && reasons.length > 0
                  ? disabledMessage
                  : suspendMessage(reasons)
              const name = nameOf(row)
              const pending = busy.has(row.orgId)
              return (
                <TableRow key={row.orgId}>
                  <TableCell className="font-medium">{name}</TableCell>
                  <TableCell>
                    <Badge variant="outline">
                      {statusLabel("tenant", status)}
                    </Badge>
                    {message && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        {message}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-sm">
                    {usageRows(row.limits, row.use).map((u) => (
                      <div
                        key={u.label}
                        className={u.over ? "text-destructive" : undefined}
                      >
                        {u.text}
                      </div>
                    ))}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Edit limits for ${name}`}
                        onClick={() => setEditing(row)}
                      >
                        Edit limits
                      </Button>
                      {enabled ? (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Disable ${name}`}
                          disabled={pending}
                          onClick={() => setAsking({ kind: "disable", row })}
                        >
                          Disable
                        </Button>
                      ) : (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Enable ${name}`}
                          disabled={pending}
                          onClick={() => setStatus(row, "enabled")}
                        >
                          Enable
                        </Button>
                      )}
                      {enabled && !reasons.includes("platform_owner") && (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Suspend ${name}`}
                          disabled={pending}
                          onClick={() => setAsking({ kind: "suspend", row })}
                        >
                          Suspend
                        </Button>
                      )}
                      {enabled && reasons.includes("platform_owner") && (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Restore ${name}`}
                          disabled={pending}
                          onClick={() =>
                            run(
                              row.orgId,
                              () =>
                                restore.mutateAsync({
                                  path: { org: row.orgId },
                                }),
                              `Workspace restored. ${accepted}`,
                              "The workspace wasn't restored."
                            )
                          }
                        >
                          Restore
                        </Button>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
      {editing && (
        <LimitsDialog row={editing} onClose={() => setEditing(null)} />
      )}
      {asking?.kind === "disable" && (
        <Confirm
          title={`Disable ${nameOf(asking.row)}?`}
          description={`${nameOf(asking.row)} can't use Loomplane Cloud until you enable it. Its running Loomplane Cloud work stops.`}
          action="Disable"
          onClose={() => setAsking(null)}
          onConfirm={() => {
            const row = asking.row
            setAsking(null)
            void setStatus(row, "disabled")
          }}
        />
      )}
      {asking?.kind === "suspend" && (
        <Confirm
          title={`Suspend ${nameOf(asking.row)}?`}
          description={`Running Loomplane Cloud work in ${nameOf(asking.row)} stops until you restore it.`}
          action="Suspend"
          onClose={() => setAsking(null)}
          onConfirm={() => {
            const row = asking.row
            setAsking(null)
            void run(
              row.orgId,
              () => suspend.mutateAsync({ path: { org: row.orgId } }),
              `Workspace suspended. ${accepted}`,
              "The workspace wasn't suspended."
            )
          }}
        />
      )}
    </section>
  )
}
