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
import { chip } from "@/components/octopus/host-card"
import { PanelLoading, ReadFailed } from "@/components/octopus/panel-states"
import { usePushRoutes } from "@/components/octopus/use-push-routes"
import { Panel, PanelHead } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
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

/** A discovered service's state as a status pill tone. */
const stateTones: Record<ReturnType<typeof discoveryState>["tone"], Tone> = {
  overdye: "live",
  weld: "waiting",
  failed: "failed",
}

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
      <Panel
        aria-label="How discovery works"
        className="flex-row flex-wrap items-center gap-x-4 gap-y-2.5 bg-subtle py-3"
      >
        <span className="flex-[1_1_360px] text-[13px]">
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
      </Panel>

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

      <Panel aria-label="How to opt in" className="gap-2.5">
        <PanelHead title="How to opt in" />
        <p className="text-[13px] text-muted-foreground">{optInLine}</p>
        <pre className="overflow-x-auto rounded-lg bg-muted px-3 py-2.5 font-mono text-[12.5px] leading-relaxed">
          {farpExample}
        </pre>
      </Panel>
    </>
  )
}

function ServiceTable({ services }: { services: DiscoveredService[] }) {
  const now = useClock()
  return (
    <Card className="py-0">
      <Table aria-label="FARP services" className="min-w-[720px]">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Service</TableHead>
            <TableHead>Schemas</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Routes</TableHead>
            <TableHead>Updated</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {services.map((s) => {
            const state = discoveryState(s.state)
            return (
              <TableRow key={s.service}>
                <TableCell data-service className="font-medium">
                  {s.service}
                </TableCell>
                <TableCell className="whitespace-normal">
                  <span className="flex min-w-0 flex-wrap gap-1">
                    {farpSchemas(s.metadata).map((path) => (
                      <span
                        key={path}
                        className={cn(
                          chip,
                          "bg-muted font-mono font-normal break-all whitespace-normal"
                        )}
                      >
                        {path}
                      </span>
                    ))}
                  </span>
                </TableCell>
                <TableCell className="whitespace-normal">
                  <span className="flex min-w-0 flex-col gap-1">
                    <StatusPill tone={stateTones[state.tone]}>
                      {state.label}
                    </StatusPill>
                    {s.state === "failed" && s.error && (
                      <span className="text-xs text-destructive">
                        {s.error}
                      </span>
                    )}
                  </span>
                </TableCell>
                <TableCell className="font-mono text-xs">{s.routes}</TableCell>
                <TableCell className="text-[12.5px] text-muted-foreground">
                  {isSet(s.updatedAt) ? relativeTime(s.updatedAt, now) : ""}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </Card>
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
    <Panel aria-label="Where FARP routes land" className="gap-2.5">
      <PanelHead title="Where FARP routes land" />
      {board.data && stranded.length > 0 ? (
        <p className="text-[12.5px] text-weld-ink">
          <StrandedFarp hosts={stranded} />
        </p>
      ) : board.data ? (
        <div className="flex flex-wrap items-center gap-2">
          {hostnames.length > 0 ? (
            hostnames.map((h) => (
              <span
                key={h}
                className={cn(
                  chip,
                  "bg-flight-tint font-mono font-normal text-flight-ink"
                )}
              >
                {h}
              </span>
            ))
          ) : (
            <span className={cn(chip, "bg-muted")}>Every hostname</span>
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
        <Skeleton className="h-5 w-48 rounded-md" />
      )}
    </Panel>
  )
}
