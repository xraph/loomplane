// The DNS records Loomplane wrote for a domain and its subdomains: each
// zone's TXT proof and each route's record, read-only, so you can see
// exactly what it wrote. Not in the mock.
import { useMemo } from "react"

import type {
  ManagedRecord,
  ZoneOverview,
} from "@/client/forge/loomplane/types"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

const columns =
  "grid grid-cols-[64px_minmax(0,1.2fr)_minmax(0,1.2fr)_48px_minmax(0,1fr)] gap-3"
// On a domain whose records sit in more than one zone, which one each is in.
const withSubdomain =
  "grid grid-cols-[64px_minmax(0,1.2fr)_minmax(0,1.2fr)_48px_minmax(0,0.9fr)_minmax(0,1fr)] gap-3"

export function ManagedRecords({
  records,
  zones,
}: {
  records: ManagedRecord[]
  /** The page's zone first, then its subdomains, to name a record's zone. */
  zones: ZoneOverview[]
}) {
  const names = useMemo(
    () =>
      new Map(
        zones.flatMap((z) =>
          z.zone ? [[z.zone.id, z.zone.name] as const] : []
        )
      ),
    [zones]
  )
  // The proof first, then by name.
  const sorted = useMemo(
    () =>
      [...records].sort(
        (a, b) =>
          Number(b.kind === "proof") - Number(a.kind === "proof") ||
          a.name.localeCompare(b.name)
      ),
    [records]
  )

  if (sorted.length === 0)
    return (
      // Not in the mock.
      <p className="px-4 py-3 text-[12.5px] text-muted-foreground">
        {zones[0]?.zone?.mode === "external"
          ? "Loomplane writes no records here. You add each hostname's record where the domain is hosted."
          : "Loomplane hasn't written any records here yet."}
      </p>
    )

  const spans = new Set(sorted.map((r) => r.zoneId)).size > 1
  const grid = spans ? withSubdomain : columns
  const headers = spans
    ? ["Type", "Name", "Value", "TTL", "Subdomain", "Owned by"]
    : ["Type", "Name", "Value", "TTL", "Owned by"]
  const mono = "font-mono text-[12.5px] break-all"
  return (
    <div className="overflow-x-auto">
      <div
        role="table"
        aria-label="DNS records"
        className={spans ? "min-w-[820px]" : "min-w-[680px]"}
      >
        <div
          role="row"
          className={cn(
            grid,
            "border-b border-border/70 px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
          )}
        >
          {headers.map((h) => (
            <span key={h} role="columnheader">
              {h}
            </span>
          ))}
        </div>
        {sorted.map((r, i) => (
          <div
            key={`${r.zoneId}:${r.kind}:${r.routeId ?? ""}:${r.type}:${r.name}`}
            role="row"
            className={cn(
              grid,
              "items-start px-4 py-2.5",
              i > 0 && "border-t border-border/70"
            )}
          >
            <span role="cell" className={mono}>
              {r.type}
            </span>
            <span role="cell" className={cn(mono, "font-medium")}>
              {r.name}
            </span>
            <span role="cell" className={cn(mono, "text-foreground/80")}>
              {r.value}
            </span>
            <span role="cell" className="font-mono text-[12.5px]">
              {r.ttl}
            </span>
            {spans && (
              <span role="cell" className={cn(mono, "text-foreground/80")}>
                {names.get(r.zoneId) ?? r.zoneId}
              </span>
            )}
            <span role="cell" className="min-w-0 text-[12.5px] break-all">
              <Owner record={r} />
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

// What a record is there for: the zone's proof, or the hostname of the
// route that wrote it, linking to its project.
function Owner({ record }: { record: ManagedRecord }) {
  // Not in the mock.
  if (record.kind === "proof")
    return <span className="text-foreground/80">Domain proof</span>
  if (!record.projectId)
    return <span className="font-mono">{record.hostname}</span>
  return (
    <Link
      to="/projects/$id"
      params={{ id: record.projectId }}
      className="font-mono text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
    >
      {record.hostname}
    </Link>
  )
}
