// The GPUs a server or cluster connection can see, read live.
import { CpuIcon } from "lucide-react"

import { useConnectionGpusGet } from "@/client/forge/loomplane/hooks"
import type { GPUDevice, GPUNodeGroup } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button, buttonVariants } from "@/components/ui/button"
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
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { formatHourly, formatMonthly, gpuDocsUrl, gpuTitle } from "@/lib/gpu"

// These statuses explain a problem in the inventory's own words.
const problems = new Set([
  "toolkit_missing",
  "driver_missing",
  "no_device_plugin",
  "unknown",
])

function kindTitle(kind: string, product?: string): string {
  const known = gpuTitle(kind)
  if (kind && known !== kind) return known
  return product || kind || "GPU, kind not labelled"
}

function gib(mib: number): string {
  return `${Number((mib / 1024).toFixed(1))} GiB`
}

export function GpuInventory({
  connectionId,
  cloud,
}: {
  connectionId: string
  cloud: "server" | "kubernetes"
}) {
  const gpus = useQuery(useConnectionGpusGet, { path: { id: connectionId } })
  const inv = gpus.data
  const devices = inv?.devices ?? []
  const nodes = inv?.nodes ?? []
  return (
    <section className="overflow-hidden rounded-xl bg-card ring-1 ring-border">
      <div className="flex items-center justify-between gap-2 border-b px-4 py-3">
        <h2 className="text-sm font-semibold">GPUs</h2>
        <Button
          size="sm"
          variant="outline"
          onClick={() => void gpus.refetch().catch(() => undefined)}
        >
          Refresh
        </Button>
      </div>
      {gpus.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={gpus.error}
            fallback="GPUs could not be read."
            retry={() => gpus.refetch()}
          />
        </div>
      ) : !inv ? (
        <div className="p-4">
          <Skeleton className="h-5 w-72" />
        </div>
      ) : inv.status === "unlisted" ? (
        <Missing
          title="Loomplane can't list this cluster's nodes"
          description={inv.message}
        />
      ) : inv.status === "none" && cloud === "kubernetes" ? (
        <Missing
          title="No GPU nodes"
          description={
            inv.message ||
            "This cluster has no nodes with GPU drivers and the device plugin."
          }
        />
      ) : inv.status === "none" ? (
        <div className="px-4 py-4 text-[13px] text-muted-foreground">
          <p>No GPUs on this server.</p>
          {inv.message && <p className="mt-1">{inv.message}</p>}
        </div>
      ) : (
        <div className="grid gap-3 p-4">
          {problems.has(inv.status) && (
            <Alert>
              <AlertDescription>{inv.message}</AlertDescription>
            </Alert>
          )}
          {devices.length > 0 && <DeviceTable devices={devices} />}
          {nodes.length > 0 && <NodeTable groups={nodes} />}
        </div>
      )}
    </section>
  )
}

function Missing({
  title,
  description,
}: {
  title: string
  description?: string
}) {
  return (
    <ZeroState surface={false} preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <CpuIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        {description && (
          <ZeroStateDescription>{description}</ZeroStateDescription>
        )}
      </ZeroStateHeader>
      <ZeroStateContent>
        <a
          href={gpuDocsUrl}
          target="_blank"
          rel="noreferrer"
          className={buttonVariants({ variant: "outline", size: "sm" })}
        >
          GPU prerequisites
        </a>
      </ZeroStateContent>
    </ZeroState>
  )
}

function DeviceTable({ devices }: { devices: GPUDevice[] }) {
  return (
    <div className="min-w-0 rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>GPU</TableHead>
            <TableHead className="w-28 text-right">Memory</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {devices.map((d) => (
            <TableRow key={d.uuid || d.index}>
              <TableCell className="whitespace-normal">
                <div className="text-sm">{kindTitle(d.kind, d.product)}</div>
                <div className="mt-1 font-mono text-xs break-all text-muted-foreground">
                  {d.index} · {d.uuid}
                </div>
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {gib(d.memoryMib)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function NodeTable({ groups }: { groups: GPUNodeGroup[] }) {
  return (
    <div className="min-w-0 rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>GPU nodes</TableHead>
            <TableHead className="w-24 text-right">GPUs</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {groups.map((g, i) => (
            <TableRow key={`${g.kind}-${g.instanceType ?? ""}-${i}`}>
              <TableCell className="whitespace-normal">
                <div className="text-sm">{kindTitle(g.kind, g.product)}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {g.memoryGib > 0 && `${g.memoryGib} GiB each`}
                  {g.memoryGib > 0 && g.instanceType && " · "}
                  {g.instanceType && (
                    <span className="font-mono">{g.instanceType}</span>
                  )}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  <Price g={g} />
                </div>
              </TableCell>
              <TableCell className="text-right align-top tabular-nums">
                <div>{g.total}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {g.nodes} {g.nodes === 1 ? "node" : "nodes"}, {g.perNode} each
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function Price({ g }: { g: GPUNodeGroup }) {
  if (!g.hourlyPriceKnown) return <span>{formatHourly(0, false)}</span>
  return (
    <>
      <div>{`${formatHourly(g.hourlyUsd, true)} per node, estimate`}</div>
      <div>{formatMonthly(g.hourlyUsd)} per node</div>
    </>
  )
}
