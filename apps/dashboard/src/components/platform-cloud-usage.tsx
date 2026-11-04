import { messageOf } from "@/client/errors"
import { useWorkspacePlatformCloudGet } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { reasonsOf } from "@/components/platform-cloud-parts"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
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

/** RunByInstance marks a Cloud the instance owner runs. It says nothing else. */
export function RunByInstance() {
  return (
    <Badge variant="outline" className="shrink-0">
      Run by this instance
    </Badge>
  )
}

/** PlatformCloudUsage shows a workspace its use, limits, regions and placements. */
export function PlatformCloudUsage() {
  const view = useQuery(useWorkspacePlatformCloudGet)

  if (view.status === "error") {
    return (
      <Alert variant="destructive" className="mb-5">
        <AlertDescription>
          {messageOf(view.error, "Loomplane Cloud use couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  }
  if (!view.data) return <Skeleton className="mb-5 h-32 w-full rounded-xl" />
  const data = view.data
  if (!data.enabled) {
    return (
      <p className="mb-5 text-sm text-muted-foreground">
        Loomplane Cloud isn&apos;t available to this workspace
      </p>
    )
  }
  const message = suspendMessage(reasonsOf(data.suspendReasons ?? []))
  const regions = data.regions ?? []
  const placements = data.placements ?? []
  return (
    <section
      aria-label="Loomplane Cloud use"
      className="mb-5 flex flex-col gap-4 rounded-xl p-4 ring-1 ring-border"
    >
      {message && (
        <Alert>
          <AlertDescription>{message}</AlertDescription>
        </Alert>
      )}
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-medium">Use and limits</h2>
        {usageRows(data.limits, data.use).map((u) => (
          <div
            key={u.label}
            className={`text-sm ${u.over ? "text-destructive" : ""}`}
          >
            {u.text}
          </div>
        ))}
      </div>
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-medium">Regions</h2>
        {regions.length === 0 ? (
          <p className="text-sm text-muted-foreground">No regions yet.</p>
        ) : (
          regions.map((r) => (
            <div key={r.name} className="flex items-center gap-2 text-sm">
              <span>{r.displayName}</span>
              <Badge variant="outline">{r.status}</Badge>
            </div>
          ))
        )}
      </div>
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-medium">Placements</h2>
        {placements.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Nothing is placed here yet.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Service</TableHead>
                <TableHead>Project</TableHead>
                <TableHead>Environment</TableHead>
                <TableHead>Region</TableHead>
                <TableHead>State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {placements.map((p) => (
                <TableRow
                  key={`${p.project}/${p.environment}/${p.service}/${p.region}`}
                >
                  <TableCell className="font-medium">{p.service}</TableCell>
                  <TableCell>{p.project}</TableCell>
                  <TableCell>{p.environment}</TableCell>
                  <TableCell>{p.region}</TableCell>
                  <TableCell>
                    {p.waitingCapacity ? "Waiting for room" : p.state}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </section>
  )
}
