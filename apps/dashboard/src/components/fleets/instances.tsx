import { LayersIcon } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { Link } from "@/lib/router"

export type InstanceRow = {
  id: string
  externalRef: string
  status: string
  profileID: string
  desiredReleaseID: string
  observedReleaseID?: string
  updatedAt: string
}

export function instanceStatus(status: string) {
  const words = status.replaceAll("_", " ")
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export function InstanceTable({
  projectID,
  rows,
  filtered = false,
  onClear,
}: {
  projectID: string
  rows: InstanceRow[]
  filtered?: boolean
  onClear?: () => void
}) {
  if (!rows.length)
    return (
      <ZeroState variant="illustrated">
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <LayersIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>
            {filtered ? "No matching instances" : "No instances yet"}
          </ZeroStateTitle>
          <ZeroStateDescription>
            {filtered
              ? "Try another reference or state."
              : "Instances appear here after a creation request is accepted. Each instance keeps its own release and environment."}
          </ZeroStateDescription>
        </ZeroStateHeader>
        <ZeroStateContent>
          {filtered ? (
            <Button variant="outline" size="sm" onClick={onClear}>
              Clear filters
            </Button>
          ) : (
            <Link
              to="/projects/$id"
              params={{ id: projectID }}
              className="text-primary underline underline-offset-4"
            >
              Open project
            </Link>
          )}
        </ZeroStateContent>
      </ZeroState>
    )
  return (
    <div
      role="region"
      aria-label="Fleet instances"
      tabIndex={0}
      className="min-w-0 overflow-x-auto rounded-lg border focus-visible:ring-2 focus-visible:ring-ring [&_[data-slot=table-container]]:overflow-visible"
    >
      <Table>
        <caption className="sr-only">
          Instances in the selected project. Replica counts, pressure and costs
          are unknown until observations are available.
        </caption>
        <TableHeader>
          <TableRow>
            {[
              "Instance",
              "State",
              "Release",
              "Profile",
              "Cloud",
              "Replicas",
              "Pressure / cost",
              "Updated",
            ].map((label) => (
              <TableHead key={label}>{label}</TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.id}>
              <TableCell>
                <Link
                  to="/projects/$id/fleet/instances/$instanceId"
                  params={{ id: projectID, instanceId: row.id }}
                  className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                >
                  {row.externalRef || row.id}
                </Link>
              </TableCell>
              <TableCell>{instanceStatus(row.status)}</TableCell>
              <TableCell>
                <span className="font-mono text-xs">
                  {row.observedReleaseID || "Unknown"}
                </span>
                {row.desiredReleaseID !== row.observedReleaseID && (
                  <div className="text-xs text-muted-foreground">
                    Target: {row.desiredReleaseID}
                  </div>
                )}
              </TableCell>
              <TableCell>{row.profileID}</TableCell>
              <TableCell className="text-muted-foreground">Unknown</TableCell>
              <TableCell className="text-muted-foreground">Unknown</TableCell>
              <TableCell className="text-muted-foreground">Unknown</TableCell>
              <TableCell>
                <time
                  dateTime={row.updatedAt}
                  className="text-xs text-muted-foreground"
                >
                  {new Date(row.updatedAt).toLocaleString()}
                </time>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
