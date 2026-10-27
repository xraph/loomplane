// The Discovery tab: how Octopus learns about services over FARP on this
// target, what it found, where their routes land and how to opt in.
import { RefreshCwIcon } from "lucide-react"

import {
  useOctopusBoard,
  useOctopusDiscovery,
} from "@/client/forge/loomplane/hooks"
import type {
  DiscoveredService,
  DiscoverySummary,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { StrandedFarp } from "@/components/octopus/discovery-strip"
import { PanelLoading, ReadFailed } from "@/components/octopus/panel-states"
import { usePushRoutes } from "@/components/octopus/use-push-routes"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateTitle,
} from "@/components/zero-state"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { isSet, relativeTime, useClock } from "@/lib/domains"
import {
  discoveryState,
  farpExample,
  farpLandings,
  farpSchemas,
  farpStranded,
} from "@/lib/octopus"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

/** The columns of a service row: service, schemas, state, routes, updated. */
const columns =
  "grid grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)_minmax(0,1.4fr)_70px_120px] gap-3"

const stateTones = {
  overdye: "bg-overdye-tint text-overdye-ink",
  weld: "bg-weld-tint text-weld-ink",
  failed: "bg-failed-tint text-destructive",
} as const

const optInLine =
  "Add a farp block to a service in the manifest. Octopus picks up its schemas on the next release."

export function DiscoveryPanel(props: CatalogPanelProps) {
  const { projectId, environmentId, resource } = props
  const discovery = useQuery(useOctopusDiscovery, {
    path: { projectId, environmentId, resource },
  })

  if (discovery.status === "error" && !discovery.data)
    return (
      <ReadFailed
        error={discovery.error}
        retry={discovery.refetch}
        denied="You can't see this gateway's discovery"
        fallback="Octopus's discovery couldn't be loaded."
      />
    )
  if (!discovery.data) return <PanelLoading label="Loading discovery" />
  return (
    <div className="grid min-w-0 gap-3.5">
      {discovery.status === "error" && (
        <QueryError
          error={discovery.error}
          fallback="Octopus's discovery couldn't be loaded."
          retry={discovery.refetch}
        />
      )}
      <Discovery {...props} discovery={discovery.data} />
    </div>
  )
}

function Discovery({
  projectId,
  environmentId,
  resource,
  discovery,
}: CatalogPanelProps & { discovery: DiscoverySummary }) {
  const { can } = useOrganization()
  const sync = usePushRoutes({ projectId, environmentId, resource })
  const push = discovery.mode === "push"

  return (
    <>
      <section
        aria-label="How discovery works"
        className="flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-[14px] border border-border bg-subtle px-4 py-3"
      >
        <span className="flex-[1_1_360px] text-[13px] leading-normal">
          {push ? (
            "A Docker server has nothing to watch, so Loomplane registers each opted-in service with Octopus on every release and sends a heartbeat every 30 seconds."
          ) : (
            <>
              Octopus watches Services labelled{" "}
              <span className="font-mono text-[12.5px]">farp.enabled=true</span>{" "}
              in namespace{" "}
              <span className="font-mono text-[12.5px]">
                {discovery.namespace}
              </span>
              , with a Role that can only get, list and watch services,
              endpoints and endpointslices there.
            </>
          )}
        </span>
        {push && can("infrastructure:modify") && (
          <Button
            type="button"
            variant="outline"
            disabled={sync.pending}
            onClick={() => void sync.push()}
          >
            <RefreshCwIcon data-icon="inline-start" aria-hidden />
            Register again
          </Button>
        )}
      </section>

      {discovery.services.length > 0 ? (
        <ServiceTable services={discovery.services} />
      ) : (
        <ZeroState>
          <ZeroStateHeader>
            <ZeroStateTitle>
              No service publishes FARP schemas yet.
            </ZeroStateTitle>
            <ZeroStateDescription>{optInLine}</ZeroStateDescription>
          </ZeroStateHeader>
        </ZeroState>
      )}

      <Landing
        projectId={projectId}
        environmentId={environmentId}
        resource={resource}
      />

      <section
        aria-label="How to opt in"
        className="grid gap-2.5 rounded-[14px] border border-border px-4 py-3.5"
      >
        <h3 className="text-sm font-semibold">How to opt in</h3>
        <p className="text-[13px] leading-normal text-foreground/80">
          {optInLine}
        </p>
        <pre className="overflow-x-auto rounded-[10px] bg-muted px-3 py-2.5 font-mono text-[12.5px] leading-[1.6] text-foreground">
          {farpExample}
        </pre>
      </section>
    </>
  )
}

function ServiceTable({ services }: { services: DiscoveredService[] }) {
  const now = useClock()
  return (
    <div className="overflow-hidden rounded-[14px] border border-border">
      <div className="overflow-x-auto">
        <div role="table" aria-label="FARP services" className="min-w-[720px]">
          <div
            role="row"
            className={cn(
              columns,
              "border-b border-border bg-subtle px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
            )}
          >
            {["Service", "Schemas", "State", "Routes", "Updated"].map((h) => (
              <span key={h} role="columnheader">
                {h}
              </span>
            ))}
          </div>
          {services.map((s) => {
            const state = discoveryState(s.state)
            return (
              <div
                key={s.service}
                role="row"
                className={cn(
                  columns,
                  "items-center border-border px-4 py-2.5 transition-colors not-first:border-t hover:bg-subtle"
                )}
              >
                <span
                  role="cell"
                  data-service
                  className="truncate text-[13px] font-medium"
                >
                  {s.service}
                </span>
                <span role="cell" className="flex min-w-0 flex-wrap gap-1">
                  {farpSchemas(s.metadata).map((path) => (
                    <span
                      key={path}
                      className="rounded-md bg-muted px-[7px] py-px font-mono text-[11.5px] break-all text-foreground/80"
                    >
                      {path}
                    </span>
                  ))}
                </span>
                <span role="cell" className="flex min-w-0 flex-col gap-1">
                  <span
                    className={cn(
                      "w-fit rounded-full px-2 py-px text-[11.5px] font-medium whitespace-nowrap",
                      stateTones[state.tone]
                    )}
                  >
                    {state.label}
                  </span>
                  {s.state === "failed" && s.error && (
                    <span className="text-xs text-destructive">{s.error}</span>
                  )}
                </span>
                <span role="cell" className="font-mono text-xs">
                  {s.routes}
                </span>
                <span
                  role="cell"
                  className="text-[12.5px] text-muted-foreground"
                >
                  {isSet(s.updatedAt) ? relativeTime(s.updatedAt, now) : ""}
                </span>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

/** Landing is where FARP routes land, from the gateway's board. */
function Landing({
  projectId,
  environmentId,
  resource,
}: {
  projectId: string
  environmentId: string
  resource: string
}) {
  // The board knows which hostnames are still attached; a picked one
  // that isn't gets no FARP routes, as the server pushes it.
  const board = useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  })
  const hostnames = board.data ? farpLandings(board.data) : []
  const stranded = board.data ? farpStranded(board.data) : []
  return (
    <section
      aria-label="Where FARP routes land"
      className="grid gap-2.5 rounded-[14px] border border-border px-4 py-3.5"
    >
      <h3 className="text-sm font-semibold">Where FARP routes land</h3>
      {board.data && stranded.length > 0 ? (
        <p className="text-[12.5px] leading-normal text-weld-ink">
          <StrandedFarp hosts={stranded} />
        </p>
      ) : board.data ? (
        <div className="flex flex-wrap items-center gap-2">
          {hostnames.length > 0 ? (
            hostnames.map((h) => (
              <span
                key={h}
                className="rounded-md bg-flight-tint px-2 py-px font-mono text-xs text-flight-ink"
              >
                {h}
              </span>
            ))
          ) : (
            <span className="rounded-md bg-muted px-2 py-px text-xs font-medium text-foreground/80">
              Every hostname
            </span>
          )}
          <span className="text-[12.5px] text-muted-foreground">
            Change on the Domains tab
          </span>
        </div>
      ) : board.status === "error" ? (
        <QueryError
          error={board.error}
          fallback="Where FARP routes land couldn't be loaded."
          retry={board.refetch}
        />
      ) : (
        <Skeleton className="h-6 w-48 rounded-md" />
      )}
    </section>
  )
}
