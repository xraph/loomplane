// One domain's card on the Routes tab: the hostname with its DNS and
// certificate, then a row per route with where it goes and where it came
// from.
import { ArrowRightIcon, PlusIcon } from "lucide-react"

import type { BoardRoute, HostCard } from "@/client/forge/loomplane/types"
import { StatusPill, type Tone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table"
import type { PillTone } from "@/lib/domains"
import {
  hostPills,
  methodsLabel,
  routeCountLabel,
  routeSourceLabel,
  type RouteSource,
} from "@/lib/octopus"
import { cn } from "@/lib/utils"

/** The Domains states as status pill tones. */
export const pillTones: Record<PillTone, Tone> = {
  ok: "live",
  warn: "waiting",
  idle: "quiet",
  failed: "failed",
  own: "flight",
}

/** A short label on a row: where a route came from, a schema, a path. */
export const chip =
  "inline-flex h-5 w-fit items-center rounded-md px-1.5 text-xs font-medium whitespace-nowrap"

const sourceTones: Record<RouteSource, string> = {
  farp: "bg-flight-tint text-flight-ink",
  here: "bg-weld-tint text-weld-ink",
  "octopus-admin": "bg-muted text-muted-foreground",
}

/** What a row's button does: override a FARP route or edit one added here. */
export type RowAction = (kind: "override" | "edit", route: BoardRoute) => void

export function HostCardView({
  host,
  privateUrl,
  onRoute,
  onRow,
}: {
  host: HostCard
  /** Where the gateway answers inside its environment. */
  privateUrl?: string
  /** Opens a new route on this host, or undefined when the viewer can't. */
  onRoute?: () => void
  onRow?: RowAction
}) {
  const title = host.hostname || "Any hostname"
  return (
    <Card role="region" aria-label={title} className="gap-0 py-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b bg-subtle px-4 py-2.5">
        <span
          className={cn(
            "text-sm font-medium break-all",
            host.hostname && "font-mono"
          )}
        >
          {title}
        </span>
        {host.status &&
          hostPills(host.status).map(([label, tone]) => (
            <StatusPill key={label} tone={pillTones[tone]}>
              {label}
            </StatusPill>
          ))}
        {host.hostname && !host.attached && (
          <StatusPill tone="waiting">Not attached</StatusPill>
        )}
        {host.farpHome && (
          <span className={cn(chip, "bg-flight-tint text-flight-ink")}>
            FARP routes land here
          </span>
        )}
        <span className="flex-1" />
        <span className="text-[12.5px] text-muted-foreground">
          {routeCountLabel(host.routes.length)}
        </span>
        {onRoute && (
          <Button type="button" variant="outline" size="sm" onClick={onRoute}>
            <PlusIcon data-icon="inline-start" aria-hidden />
            Route
          </Button>
        )}
        {!host.hostname && privateUrl && (
          <span className="basis-full text-[12.5px] text-muted-foreground">
            Reachable on the target&apos;s private network at{" "}
            <span className="font-mono">{privateUrl}</span>
          </span>
        )}
      </div>
      {/* The rows keep their columns and scroll inside the card, so a
          narrow screen never scrolls the page sideways. */}
      <Table
        aria-label={`Routes on ${title}`}
        className="min-w-[760px] table-fixed"
      >
        <colgroup>
          <col />
          <col className="w-[110px]" />
          <col className="w-[22%]" />
          <col className="w-[200px]" />
          <col className="w-[110px]" />
        </colgroup>
        <TableBody>
          {host.routes.map((route) => (
            <RouteRow key={route.key} route={route} onRow={onRow} />
          ))}
        </TableBody>
      </Table>
    </Card>
  )
}

function RouteRow({ route, onRow }: { route: BoardRoute; onRow?: RowAction }) {
  const source = route.source as RouteSource
  // The server owns the note, strip line included.
  const note = route.note
  const action =
    source === "farp" ? "override" : source === "here" ? "edit" : undefined
  return (
    <TableRow>
      <TableCell className="whitespace-normal">
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="font-mono text-[13px] font-medium break-all">
            {route.path}
          </span>
          {note && (
            <span className="text-xs text-muted-foreground">{note}</span>
          )}
          {route.sync === "pending" && (
            <span className="text-xs text-weld-ink">Pushing to Octopus</span>
          )}
          {route.sync === "failed" && route.syncError && (
            <span className="text-xs text-destructive">{route.syncError}</span>
          )}
        </span>
      </TableCell>
      <TableCell className="font-mono text-xs whitespace-normal">
        {methodsLabel(route.methods)}
      </TableCell>
      <TableCell>
        <span className="flex min-w-0 items-center gap-2">
          <ArrowRightIcon
            aria-hidden
            className="size-3.5 flex-none text-muted-foreground"
          />
          <span className="truncate font-medium">{route.service}</span>
          {route.port > 0 && (
            <span className="font-mono text-xs text-muted-foreground">
              :{route.port}
            </span>
          )}
        </span>
      </TableCell>
      <TableCell className="whitespace-normal">
        <span className="flex flex-wrap gap-1">
          <span
            className={cn(
              chip,
              sourceTones[source] ?? sourceTones["octopus-admin"]
            )}
          >
            {routeSourceLabel(source) ?? route.source}
          </span>
          {route.overrides && (
            <span className={cn(chip, "bg-muted text-foreground")}>
              Overrides FARP
            </span>
          )}
        </span>
      </TableCell>
      <TableCell className="text-right">
        {action && onRow && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onRow(action, route)}
          >
            {action === "override" ? "Override" : "Edit"}
          </Button>
        )}
      </TableCell>
    </TableRow>
  )
}
