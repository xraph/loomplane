// Where GPUs can run and what they cost per hour: the servers and clusters
// an organization connected, and the GPU sizes it could create. A model's
// target picker passes onSelect; the Infrastructure page shows it read-only.
import {
  useConnectionsList,
  useGpuTargetsList,
} from "@/client/forge/loomplane/hooks"
import type { GPUTarget } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { SectionHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { cloudLabel } from "@/lib/clouds"
import {
  acceleratorLabel,
  formatHourly,
  formatMonthly,
  priceKnown,
} from "@/lib/gpu"

export type GpuTarget = GPUTarget

// The list's query, shared so the Infrastructure page and its GPU list read
// one cached response.
function params(minMemoryGib?: number, count?: number) {
  if (!minMemoryGib && !count) return undefined
  return {
    query: {
      ...(minMemoryGib ? { min_memory_gib: minMemoryGib } : {}),
      ...(count ? { count } : {}),
    },
  }
}

function rowKey(t: GPUTarget) {
  return `${t.source}:${t.connectionId}:${t.sizeId ?? ""}:${t.accelerator.kind}`
}

export function GpuTargets({
  minMemoryGib,
  count,
  onSelect,
  source,
}: {
  minMemoryGib?: number
  count?: number
  onSelect?: (t: GpuTarget) => void
  /** Only rows from this source: connected targets, or sizes to create. */
  source?: "connection" | "server_size"
}) {
  const targets = useQuery(useGpuTargetsList, params(minMemoryGib, count))
  const connections = useQuery(useConnectionsList)
  const accounts = new Map(
    (connections.data?.connections ?? []).map((c) => [c.id, c.name])
  )
  const rows = (targets.data?.targets ?? []).filter(
    (t) => !source || t.source === source
  )
  const unreachable =
    source === "connection" ? [] : (targets.data?.unreachable ?? [])
  if (targets.status === "error")
    return (
      <QueryError
        error={targets.error}
        fallback="GPU targets couldn't be loaded."
        retry={() => targets.refetch()}
      />
    )
  if (!targets.data) return <Skeleton className="h-24 w-full rounded-xl" />
  return (
    <div className="grid min-w-0 gap-2">
      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          Nothing connected has GPUs that fit, and no account can create a GPU
          server that does.
        </p>
      ) : (
        <Card className="py-0">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Where</TableHead>
                <TableHead className="text-right">Cost</TableHead>
                {onSelect && (
                  <TableHead className="w-0">
                    <span className="sr-only">Choose</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((t) => (
                <TargetRow
                  key={rowKey(t)}
                  target={t}
                  account={accounts.get(t.connectionId)}
                  onSelect={onSelect}
                />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      {unreachable.map((u) => (
        <p key={u.connectionId} className="text-xs text-weld-ink">
          {u.name}: {u.message}
        </p>
      ))}
    </div>
  )
}

function TargetRow({
  target: t,
  account,
  onSelect,
}: {
  target: GPUTarget
  account?: string
  onSelect?: (t: GpuTarget) => void
}) {
  const size = t.source === "server_size"
  const where = size
    ? `New server in ${account ?? t.connectionId}${t.regions?.length ? ` · ${t.regions.join(", ")}` : ""}`
    : cloudLabel(t.cloud)
  return (
    <TableRow>
      <TableCell className="align-top whitespace-normal">
        <div className={size ? "font-mono text-xs font-medium" : "font-medium"}>
          {t.name}
        </div>
        <div className="mt-1 text-xs break-words text-muted-foreground">
          {where}
        </div>
        <div className="mt-1 text-xs text-muted-foreground">
          {t.accelerator.kind
            ? `${acceleratorLabel(t.accelerator)}${t.accelerator.memoryGib ? ` · ${t.accelerator.memoryGib} GiB each` : ""}`
            : "GPU kind not labelled"}
          {!size && ` · ${t.free} free`}
        </div>
        {!t.fits && t.message && (
          <div className="mt-1 text-xs text-weld-ink">{t.message}</div>
        )}
      </TableCell>
      <TableCell className="text-right align-top text-xs whitespace-normal">
        <Price target={t} />
      </TableCell>
      {onSelect && (
        <TableCell className="align-top">
          {t.fits && (
            <Button
              size="sm"
              variant="outline"
              aria-label={`Choose ${t.name}`}
              onClick={() => onSelect(t)}
            >
              Choose
            </Button>
          )}
        </TableCell>
      )}
    </TableRow>
  )
}

function Price({ target: t }: { target: GPUTarget }) {
  if (!priceKnown(t.hourlyUsd, t.hourlyPriceKnown))
    return <span className="text-muted-foreground">Price unknown</span>
  const hourly = formatHourly(t.hourlyUsd, true)
  return (
    <>
      <div className="tabular-nums">
        {t.priceEstimate ? `${hourly} per node, estimate` : hourly}
      </div>
      <div className="mt-1 text-muted-foreground tabular-nums">
        {formatMonthly(t.hourlyUsd)}
        {t.priceEstimate && " per node"}
      </div>
    </>
  )
}

// The Infrastructure page's GPU section: connected servers and clusters
// with GPUs, and what the GPU servers among them cost together. It shows
// only once something connected has GPUs.
export function InfrastructureGpus() {
  const targets = useQuery(useGpuTargetsList, params())
  const rows = (targets.data?.targets ?? []).filter(
    (t) => t.source === "connection"
  )
  if (!rows.length) return null
  const servers = new Map<string, GPUTarget>()
  for (const t of rows)
    if (t.cloud === "server" && !servers.has(t.connectionId))
      servers.set(t.connectionId, t)
  const priced = [...servers.values()].filter((t) =>
    priceKnown(t.hourlyUsd, t.hourlyPriceKnown)
  )
  const unpriced = [...servers.values()].filter(
    (t) => !priceKnown(t.hourlyUsd, t.hourlyPriceKnown)
  )
  const total = priced.reduce((sum, t) => sum + t.hourlyUsd, 0)
  return (
    <section id="gpus" className="grid scroll-mt-16 gap-3">
      <SectionHeading
        title="GPUs"
        description="Connected servers and clusters with GPUs, how many are free and what they cost"
      />
      {servers.size > 0 && (
        <div className="grid gap-1 text-sm">
          {priced.length > 0 && (
            <p>
              Running GPU servers cost about {formatHourly(total, true)}{" "}
              together.
            </p>
          )}
          {unpriced.length > 0 && (
            <p className="text-muted-foreground">
              {priced.length > 0
                ? "That leaves out"
                : "Loomplane doesn't know what"}{" "}
              {unpriced.map((t) => t.name).join(", ")}
              {priced.length > 0
                ? `, whose ${unpriced.length === 1 ? "price" : "prices"} Loomplane doesn't know.`
                : ` cost${unpriced.length === 1 ? "s" : ""} an hour.`}
            </p>
          )}
        </div>
      )}
      <GpuTargets source="connection" />
    </section>
  )
}
