import { DatabaseIcon } from "lucide-react"
import { useState } from "react"
import { useQuery } from "@/client/hooks"
import {
  useConnectionStorageList,
  useConnectionsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Project, StoredVolume } from "@/client/forge/loomplane/types"
import { QueryError } from "@/components/query-error"
import { SectionHeading } from "@/components/page"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
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
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
} from "@/components/zero-state"
import { Link } from "@/lib/router"

export function PersistentStorage({
  environmentNames = new Map(),
}: {
  environmentNames?: ReadonlyMap<string, string>
}) {
  const connections = useQuery(useConnectionsList)
  const projects = useQuery(useProjectsList)
  const targets =
    connections.data?.connections.filter(
      (c) => c.cloud === "server" || c.cloud === "kubernetes"
    ) ?? []
  const [target, setTarget] = useState("")
  const selected =
    targets.find((c) => c.id === target)?.id ??
    targets.find((c) => c.status === "ready")?.id ??
    targets[0]?.id
  const failed = [connections, projects].find(
    (query) => query.status === "error"
  )
  return (
    <section
      id="persistent-storage"
      className="grid min-w-0 scroll-mt-16 gap-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeading
          title="Persistent storage"
          description="Owned volumes and claims, including data retained after workload removal."
        />
        {selected && (
          <Select
            value={selected}
            onValueChange={(value) => setTarget(value ?? "")}
          >
            <SelectTrigger
              aria-label="Storage target"
              className="w-full sm:w-60"
            >
              <SelectValue>
                {targets.find((c) => c.id === selected)?.name}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {targets.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>
      {failed ? (
        <QueryError
          error={failed.error}
          fallback="Storage targets could not be loaded."
          retry={() => failed.refetch()}
        />
      ) : !connections.data || !projects.data ? (
        <Skeleton className="h-24" />
      ) : selected ? (
        <StorageVolumes
          key={selected}
          connection={selected}
          projects={projects.data.projects}
          environmentNames={environmentNames}
        />
      ) : (
        <StorageEmpty
          title="No storage targets connected"
          description="Connect a Docker server or Kubernetes namespace to inspect its persistent storage."
          to="/integrations"
          action="Connect infrastructure"
        />
      )}
    </section>
  )
}

function StorageEmpty({
  title,
  description,
  to,
  action,
}: {
  title: string
  description: string
  to: "/services" | "/integrations"
  action: string
}) {
  return (
    <ZeroState className="border">
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <DatabaseIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      <ZeroStateContent>
        <Link
          to={to}
          className={buttonVariants({ variant: "outline", size: "sm" })}
        >
          {action}
        </Link>
      </ZeroStateContent>
    </ZeroState>
  )
}

const statusLabels: Record<string, string> = {
  "in-use": "In use",
  retained: "Retained",
  pending: "Pending",
  lost: "Lost",
  deleting: "Deleting",
}
function capacity(volume: StoredVolume) {
  return volume.capacityBytes > 0
    ? `${(volume.capacityBytes / (1 << 20)).toLocaleString()} MiB`
    : volume.provider === "server"
      ? "Shared host capacity"
      : "Not reported"
}
function StorageVolumes({
  connection,
  projects,
  environmentNames,
}: {
  connection: string
  projects: Project[]
  environmentNames: ReadonlyMap<string, string>
}) {
  const inventory = useQuery(useConnectionStorageList, {
    path: { id: connection },
  })
  const volumes = inventory.data?.volumes ?? []
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <p>
          Detached data is not a backup. In use includes stopped containers and
          completed pods.
        </p>
        <Button
          size="sm"
          variant="outline"
          onClick={() => void inventory.refetch().catch(() => undefined)}
        >
          Refresh storage
        </Button>
      </div>
      {inventory.status === "error" ? (
        <QueryError
          error={inventory.error}
          fallback="Storage could not be inspected."
          retry={() => inventory.refetch()}
        />
      ) : !inventory.data ? (
        <Skeleton className="h-24" />
      ) : !volumes.length ? (
        <StorageEmpty
          title="No persistent volumes found"
          description="This target has no visible Loomplane-owned storage. Deploy a service with persistent storage to create a volume."
          to="/services"
          action="Deploy a service"
        />
      ) : (
        <div className="min-w-0 rounded-lg border">
          <Table className="table-fixed">
            <TableHeader>
              <TableRow>
                <TableHead className="w-[65%] sm:w-[40%]">Volume</TableHead>
                <TableHead className="hidden sm:table-cell">
                  Project / environment
                </TableHead>
                <TableHead className="w-24">Status</TableHead>
                <TableHead className="hidden w-40 lg:table-cell">
                  Capacity
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {volumes.map((volume) => {
                const project =
                  projects.find((p) => p.id === volume.projectId)?.name ??
                  volume.projectId
                return (
                  <TableRow key={volume.id}>
                    <TableCell className="max-w-0 whitespace-normal">
                      <div className="font-mono text-xs break-all">
                        {volume.name}
                      </div>
                      <div className="mt-1 text-xs text-muted-foreground">
                        {volume.role} · {volume.storageClass || volume.provider}
                      </div>
                      <div className="mt-1 text-xs break-all text-muted-foreground sm:hidden">
                        {project} ·{" "}
                        {environmentNames.get(volume.environmentId) ??
                          volume.environmentId}
                      </div>
                      <div className="mt-1 text-xs text-muted-foreground lg:hidden">
                        {capacity(volume)}
                      </div>
                    </TableCell>
                    <TableCell className="hidden max-w-0 whitespace-normal sm:table-cell">
                      <div className="text-sm break-words">{project}</div>
                      <div className="font-mono text-xs break-all text-muted-foreground">
                        {environmentNames.get(volume.environmentId) ??
                          volume.environmentId}
                      </div>
                    </TableCell>
                    <TableCell className="w-24 align-top">
                      <Badge
                        variant={
                          volume.status === "lost" ? "destructive" : "outline"
                        }
                      >
                        {statusLabels[volume.status] ?? volume.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="hidden text-xs lg:table-cell">
                      {capacity(volume)}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  )
}
