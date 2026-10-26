import { useEffect, useState } from "react"
import { toast } from "sonner"

import { useMutation, useQuery } from "@/client/hooks"
import {
  useDbGatewaysList,
  useDbGatewaysPauseRoute,
  useDbGatewaysResumeRoute,
} from "@/client/forge/loomplane/hooks"
import type {
  DbgatewayRouteStatus,
  GatewayStatus,
  InstanceStatus,
} from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { DotProgress } from "@/components/dots"
import { QueryError } from "@/components/query-error"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"

// Instances report every 10 seconds; a look this often keeps staleness
// close to what the control plane sees.
const refreshEvery = 15_000

/**
 * DatabaseGateways shows the project's database gateways: each instance's
 * snapshot and whether it still reports, and each route's connections,
 * reachability and pin rate, with pause and resume for admins. A project
 * without a gateway renders nothing.
 */
export function DatabaseGateways({ project }: { project: string }) {
  const list = useQuery(useDbGatewaysList, { path: { id: project } })
  const { refetch } = list
  useEffect(() => {
    const id = window.setInterval(
      () => void refetch().catch(() => undefined),
      refreshEvery
    )
    return () => window.clearInterval(id)
  }, [refetch])
  if (list.status === "error")
    return (
      <QueryError
        error={list.error}
        fallback="Could not load database gateways"
        retry={refetch}
      />
    )
  const gateways = list.data?.gateways ?? []
  if (!gateways.length) return null
  return (
    <section aria-label="Database gateways" className="grid gap-4">
      {gateways.map((g) => (
        <GatewayCard
          key={g.id}
          project={project}
          gateway={g}
          reload={refetch}
        />
      ))}
    </section>
  )
}

function GatewayCard({
  project,
  gateway,
  reload,
}: {
  project: string
  gateway: GatewayStatus
  reload: () => Promise<unknown>
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Database gateway {gateway.name}</CardTitle>
        <CardDescription>
          {gateway.provider} · {gateway.region} · snapshot{" "}
          {gateway.snapshotVersion}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <Instances instances={gateway.instances} />
        <Routes project={project} gateway={gateway} reload={reload} />
      </CardContent>
    </Card>
  )
}

function Instances({ instances }: { instances: InstanceStatus[] }) {
  if (!instances.length)
    return (
      <p className="text-muted-foreground">
        No instance has reported in the last hour.
      </p>
    )
  return (
    <ul aria-label="Instances" className="grid gap-1.5">
      {instances.map((i) => (
        <li key={i.id} className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs">{i.id}</span>
          <span className="text-xs text-muted-foreground">
            snapshot {i.snapshotVersion}, last report {formatDate(i.lastSeenAt)}
          </span>
          {!i.reporting && <Badge variant="destructive">Not reporting</Badge>}
          {i.behind && <Badge variant="secondary">Behind</Badge>}
          {i.draining && <Badge variant="outline">Draining</Badge>}
        </li>
      ))}
    </ul>
  )
}

function Routes({
  project,
  gateway,
  reload,
}: {
  project: string
  gateway: GatewayStatus
  reload: () => Promise<unknown>
}) {
  const { can } = useOrganization()
  const admin = can("infrastructure:modify")
  const pause = useMutation(useDbGatewaysPauseRoute)
  const resume = useMutation(useDbGatewaysResumeRoute)
  const [busy, setBusy] = useState("")
  // The list is the only source of a route's state: after a pause or
  // resume the card reads it again rather than trusting the answer, so it
  // shows what the server holds even if something else changed it since.
  async function toggle(route: DbgatewayRouteStatus, paused: boolean) {
    setBusy(route.id)
    try {
      const vars = {
        path: { id: project, gatewayId: gateway.id, routeId: route.id },
      }
      if (paused) await resume.mutateAsync(vars)
      else await pause.mutateAsync(vars)
    } catch (error) {
      toast.error(messageOf(error, "The route could not be changed"))
    }
    try {
      await reload()
    } catch {
      // The list shows its own error.
    } finally {
      setBusy("")
    }
  }
  if (!gateway.routes.length)
    return <p className="text-muted-foreground">No database routes yet.</p>
  return (
    <div className="min-w-0 overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Database</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Connections</TableHead>
            <TableHead className="hidden sm:table-cell">Pin rate</TableHead>
            {admin && <TableHead className="text-right">Actions</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {gateway.routes.map((r) => {
            const paused = r.state === "paused"
            return (
              <TableRow key={r.id}>
                <TableCell className="font-medium">{r.resource}</TableCell>
                <TableCell>
                  <span className="flex flex-wrap gap-1.5">
                    {paused ? (
                      <Badge variant="secondary">Paused</Badge>
                    ) : (
                      <Badge variant="outline">Active</Badge>
                    )}
                    {r.reported && !r.reachable && (
                      <Badge variant="destructive">Unreachable</Badge>
                    )}
                  </span>
                </TableCell>
                <TableCell>
                  {r.reported
                    ? `${r.backendConns} of ${r.maxConnections}`
                    : "No report"}
                </TableCell>
                <TableCell className="hidden sm:table-cell">
                  {r.reported && (
                    <span className="flex items-center gap-2">
                      <DotProgress
                        size="sm"
                        value={r.pinRate}
                        label={`Pin rate of ${r.resource}`}
                        className="w-20"
                      />
                      <span className="text-xs text-muted-foreground">
                        {Math.round(r.pinRate * 100)}%
                      </span>
                    </span>
                  )}
                </TableCell>
                {admin && (
                  <TableCell className="text-right">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={busy === r.id}
                      onClick={() => void toggle(r, paused)}
                    >
                      {paused ? "Resume" : "Pause"}
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}
