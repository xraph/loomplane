// One domain's card on the Routes tab: the hostname with its DNS and
// certificate, then a row per route with where it goes and where it came
// from.
import { ArrowRightIcon, PlusIcon } from "lucide-react"

import type { BoardRoute, HostCard } from "@/client/forge/loomplane/types"
import { DomainStatePill } from "@/components/domains/domain-states"
import { Button } from "@/components/ui/button"
import {
  hostPills,
  methodsLabel,
  routeCountLabel,
  routeSourceLabel,
  type RouteSource,
} from "@/lib/octopus"
import { cn } from "@/lib/utils"

/** The columns of a route row, from the mock. */
const routeGrid =
  "grid grid-cols-[minmax(0,1.5fr)_110px_minmax(0,1.1fr)_190px_90px] items-center gap-3"

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
    <section
      aria-label={title}
      className="overflow-hidden rounded-[14px] border border-border"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border bg-subtle px-4 py-[11px]">
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
            <DomainStatePill key={label} tone={tone} className="text-xs">
              {label}
            </DomainStatePill>
          ))}
        {host.hostname && !host.attached && (
          <DomainStatePill tone="warn" className="text-xs">
            Not attached
          </DomainStatePill>
        )}
        {host.farpHome && (
          <span className="rounded-md bg-flight-tint px-2 py-px text-xs font-medium text-flight-ink">
            FARP routes land here
          </span>
        )}
        <span className="flex-1" />
        <span className="text-[12.5px] text-muted-foreground">
          {routeCountLabel(host.routes.length)}
        </span>
        {onRoute && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="text-xs"
            onClick={onRoute}
          >
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
      <div className="overflow-x-auto">
        <div
          role="table"
          aria-label={`Routes on ${title}`}
          className="min-w-[760px]"
        >
          {host.routes.map((route) => (
            <RouteRow key={route.key} route={route} onRow={onRow} />
          ))}
        </div>
      </div>
    </section>
  )
}

function RouteRow({ route, onRow }: { route: BoardRoute; onRow?: RowAction }) {
  const source = route.source as RouteSource
  // The server owns the note, strip line included.
  const note = route.note
  const action =
    source === "farp" ? "override" : source === "here" ? "edit" : undefined
  return (
    <div
      role="row"
      className={cn(
        routeGrid,
        "border-border px-4 py-2.5 transition-colors not-first:border-t hover:bg-subtle"
      )}
    >
      <span role="cell" className="flex min-w-0 flex-col gap-0.5">
        <span className="font-mono text-[13px] font-medium break-all">
          {route.path}
        </span>
        {note && <span className="text-xs text-muted-foreground">{note}</span>}
        {route.sync === "pending" && (
          <span className="text-xs text-weld-ink">Pushing to Octopus</span>
        )}
        {route.sync === "failed" && route.syncError && (
          <span className="text-xs text-destructive">{route.syncError}</span>
        )}
      </span>
      <span role="cell" className="font-mono text-xs text-foreground/80">
        {methodsLabel(route.methods)}
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-2 text-[13px]">
        <ArrowRightIcon
          aria-hidden
          className="size-3.5 flex-none text-muted-foreground/70"
        />
        <span className="truncate font-medium">{route.service}</span>
        {route.port > 0 && (
          <span className="font-mono text-xs text-muted-foreground">
            :{route.port}
          </span>
        )}
      </span>
      <span role="cell" className="flex flex-wrap gap-1">
        <span
          className={cn(
            "rounded-md px-[7px] py-px text-[11.5px] font-medium",
            sourceTones[source] ?? sourceTones["octopus-admin"]
          )}
        >
          {routeSourceLabel(source) ?? route.source}
        </span>
        {route.overrides && (
          <span className="rounded-md bg-muted px-[7px] py-px text-[11.5px] font-medium text-foreground/80">
            Overrides FARP
          </span>
        )}
      </span>
      <span role="cell" className="flex justify-end">
        {action && onRow && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-7 text-xs"
            onClick={() => onRow(action, route)}
          >
            {action === "override" ? "Override" : "Edit"}
          </Button>
        )}
      </span>
    </div>
  )
}
