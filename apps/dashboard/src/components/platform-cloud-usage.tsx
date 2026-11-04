import { statusOf } from "@/client/errors"
import { useWorkspacePlatformCloudGet } from "@/client/forge/loomplane/hooks"
import type { EnvironmentUsage, Project } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  errorOf,
  PlatformCloudEmpty,
  PlatformCloudStatus,
} from "@/components/platform-cloud-parts"
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
      <PlatformCloudEmpty title="You can't see this Cloud's use.">
        Ask a workspace owner for permission to view usage.
      </PlatformCloudEmpty>
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
      <PlatformCloudEmpty title="Loomplane Cloud isn't available to this workspace">
        The instance owner manages access to this Cloud.
      </PlatformCloudEmpty>
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
          <PlatformCloudEmpty title="No regions yet.">
            Regions appear here when the instance owner makes them available.
          </PlatformCloudEmpty>
        ) : (
          regions.map((r) => (
            <div key={r.name} className="flex items-center gap-2 text-sm">
              <span>{r.displayName}</span>
              <PlatformCloudStatus kind="region" status={r.status} />
            </div>
          ))
        )}
      </div>
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-medium">Placements</h2>
        {placements.length === 0 ? (
          <PlatformCloudEmpty title="Nothing is placed here yet.">
            Services appear here after you deploy to this Cloud.
          </PlatformCloudEmpty>
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
                    <PlatformCloudStatus
                      kind="placement"
                      status={p.state}
                      waitingCapacity={p.waitingCapacity}
                    />
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
