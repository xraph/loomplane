import { statusOf } from "@/client/errors"
import { useWorkspacePlatformCloudGet } from "@/client/forge/loomplane/hooks"
import type { EnvironmentUsage, Project } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { errorOf } from "@/components/platform-cloud-parts"
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
import { statusLabel, suspendMessage, usageRows } from "@/lib/platform-cloud"

/** RunByInstance marks a Cloud the instance owner runs. It says nothing else. */
export function RunByInstance() {
  return (
    <Badge variant="outline" className="shrink-0">
      Run by this instance
    </Badge>
  )
}

/**
 * PlatformCloudUsage shows a workspace its use, limits, regions and
 * placements. Placements name their project and environment from the lists
 * the Cloud page already has; in a project's view only that project's show.
 */
export function PlatformCloudUsage({
  project,
  projects = [],
  environments = [],
}: {
  project?: string
  projects?: Project[]
  environments?: EnvironmentUsage[]
}) {
  const view = useQuery(useWorkspacePlatformCloudGet)

  if (view.status === "error" && statusOf(view.error) === 403) {
    return (
      <p className="mb-5 text-sm text-muted-foreground">
        You can&apos;t see this Cloud&apos;s use.
      </p>
    )
  }
  if (view.status === "error") {
    return (
      <Alert variant="destructive" className="mb-5">
        <AlertDescription>
          {errorOf(view.error, "Loomplane Cloud use couldn't be loaded.")}
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
  const message = suspendMessage(data.suspendReasons ?? [])
  const regions = data.regions ?? []
  const placements = (data.placements ?? []).filter(
    (p) => !project || p.project === project
  )
  const projectName = (id: string) =>
    projects.find((p) => p.id === id)?.name ??
    environments.find((e) => e.projectId === id)?.projectName ??
    id
  const environmentName = (id: string) =>
    environments.find((e) => e.environmentId === id)?.environmentName ?? id
  const regionName = (name: string) =>
    regions.find((r) => r.name === name)?.displayName ?? name
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
              <Badge variant="outline">{statusLabel("region", r.status)}</Badge>
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
              {placements.map((p, i) => (
                // A replacement can run beside the current placement, so
                // the same service can appear twice.
                <TableRow
                  key={`${p.project}/${p.environment}/${p.service}/${p.region}/${p.state}/${i}`}
                >
                  <TableCell className="font-medium">{p.service}</TableCell>
                  <TableCell>{projectName(p.project)}</TableCell>
                  <TableCell>{environmentName(p.environment)}</TableCell>
                  <TableCell>{regionName(p.region)}</TableCell>
                  <TableCell>
                    {p.waitingCapacity
                      ? "Waiting for room"
                      : statusLabel("placement", p.state)}
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
