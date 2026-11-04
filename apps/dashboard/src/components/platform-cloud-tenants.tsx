import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlatformCloudTenantsList,
  usePlatformCloudTenantsRestore,
  usePlatformCloudTenantsSet,
  usePlatformCloudTenantsSuspend,
} from "@/client/forge/loomplane/hooks"
import type { TenantRow } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { LimitsDialog } from "@/components/platform-cloud-limits-dialog"
import { amountsOf, reasonsOf } from "@/components/platform-cloud-parts"
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
import { suspendMessage, usageRows } from "@/lib/platform-cloud"

const accepted = "Recorded. Running work may take a moment to stop or start."

/** PlatformCloudTenants lists workspaces with use against limits and their controls. */
export function PlatformCloudTenants() {
  const tenants = useQuery(usePlatformCloudTenantsList)
  const set = useMutation(usePlatformCloudTenantsSet)
  const suspend = useMutation(usePlatformCloudTenantsSuspend)
  const restore = useMutation(usePlatformCloudTenantsRestore)
  const invalidate = useInvalidate()
  const [editing, setEditing] = useState<TenantRow | null>(null)
  const [error, setError] = useState("")

  const run = async (
    work: () => Promise<unknown>,
    done: string,
    fail: string
  ) => {
    setError("")
    try {
      await work()
      invalidate(usePlatformCloudTenantsList)
      toast.success(done)
    } catch (err) {
      setError(messageOf(err, fail))
    }
  }

  const setStatus = (row: TenantRow, status: "enabled" | "disabled") =>
    run(
      () =>
        set.mutateAsync({
          path: { org: row.orgId },
          body: {
            status,
            ...(row.tenant?.limits ? { limits: row.tenant.limits } : {}),
          },
        }),
      status === "enabled"
        ? "Workspace enabled"
        : `Workspace disabled. ${accepted}`,
      "The workspace wasn't changed."
    )

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-base font-medium">Workspaces</h2>
      {(error || tenants.status === "error") && (
        <Alert variant="destructive">
          <AlertDescription>
            {error ||
              messageOf(tenants.error, "Workspaces couldn't be loaded.")}
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
              const reasons = reasonsOf(row.tenant?.suspendReasons ?? [])
              const message = suspendMessage(reasons)
              const enabled = status === "enabled"
              return (
                <TableRow key={row.orgId}>
                  <TableCell className="font-medium">{row.orgName}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{status}</Badge>
                    {message && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        {message}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-sm">
                    {usageRows(amountsOf(row.limits), amountsOf(row.use)).map(
                      (u) => (
                        <div
                          key={u.label}
                          className={u.over ? "text-destructive" : undefined}
                        >
                          {u.text}
                        </div>
                      )
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Edit limits for ${row.orgName}`}
                        onClick={() => setEditing(row)}
                      >
                        Edit limits
                      </Button>
                      {enabled ? (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Disable ${row.orgName}`}
                          onClick={() => setStatus(row, "disabled")}
                        >
                          Disable
                        </Button>
                      ) : (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Enable ${row.orgName}`}
                          onClick={() => setStatus(row, "enabled")}
                        >
                          Enable
                        </Button>
                      )}
                      {enabled && !reasons.includes("platform_owner") && (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Suspend ${row.orgName}`}
                          onClick={() =>
                            run(
                              () =>
                                suspend.mutateAsync({
                                  path: { org: row.orgId },
                                }),
                              `Workspace suspended. ${accepted}`,
                              "The workspace wasn't suspended."
                            )
                          }
                        >
                          Suspend
                        </Button>
                      )}
                      {enabled && reasons.includes("platform_owner") && (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Restore ${row.orgName}`}
                          onClick={() =>
                            run(
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
    </section>
  )
}
