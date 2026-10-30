// Every hostname a project points at a domain, with where each runs and
// how far its DNS, certificate and route have got. Built to the Domains
// mock's table.
import { useMemo } from "react"

import { useEnvironmentsList } from "@/client/forge/loomplane/hooks"
import type { HostnameUse } from "@/client/forge/loomplane/types"
import { useQueries } from "@/client/queries"
import { RouteStates } from "@/components/domains/domain-states"
import { EdgeCacheButton } from "@/components/domains/edge-cache-dialog"
import { useOrganization } from "@/lib/organization"
import { runsOnLabel, usedByLabel } from "@/lib/domains"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

// The mock's columns: Hostname, Used by, Runs on, DNS, Certificate, Route.
const columns =
  "grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_170px_96px_140px_140px] gap-3"
// Not in the mock: on a domain with subdomains, which one a hostname is under.
const withSubdomain =
  "grid grid-cols-[minmax(0,1fr)_minmax(0,0.9fr)_minmax(0,1.2fr)_170px_96px_140px_140px] gap-3"

export function HostnameTable({
  hostnames,
  subdomain,
}: {
  hostnames: HostnameUse[]
  /** Names the zone a hostname is under, for a Subdomain column. */
  subdomain?: (h: HostnameUse) => string
}) {
  const { can } = useOrganization()
  // The overview names environments by ID; their projects name them.
  const projects = useMemo(
    () =>
      [...new Set(hostnames.map((h) => h.projectId))].map((id) => ({
        path: { id },
      })),
    [hostnames]
  )
  const environments = useQueries(useEnvironmentsList, projects)
  const envNames = useMemo(
    () =>
      new Map(
        environments.states.flatMap((s) =>
          (s.data?.environments ?? []).map((e) => [e.id, e.name] as const)
        )
      ),
    [environments.states]
  )
  const grid = subdomain ? withSubdomain : columns
  const headers = subdomain
    ? [
        "Hostname",
        "Subdomain",
        "Used by",
        "Runs on",
        "DNS",
        "Certificate",
        "Route",
      ]
    : ["Hostname", "Used by", "Runs on", "DNS", "Certificate", "Route"]
  return (
    <div className="overflow-x-auto">
      <div
        role="table"
        aria-label="Hostnames"
        className={subdomain ? "min-w-[1000px]" : "min-w-[860px]"}
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
        {hostnames.map((h, i) => (
          <div
            key={h.routeId}
            role="row"
            className={cn(
              grid,
              "items-center px-4 py-2.5 transition-colors hover:bg-subtle",
              i > 0 && "border-t border-border/70"
            )}
          >
            <span
              role="cell"
              className="grid min-w-0 gap-1 text-[12.5px] font-medium"
            >
              <span className="truncate font-mono">{h.hostname}</span>
              <span className="justify-self-start">
                <EdgeCacheButton
                  projectId={h.projectId}
                  routeId={h.routeId}
                  hostname={h.hostname}
                  writable={can("infrastructure:modify")}
                />
              </span>
            </span>
            {subdomain && (
              <span
                role="cell"
                className="truncate font-mono text-[12.5px] text-foreground/80"
              >
                {subdomain(h)}
              </span>
            )}
            <span role="cell" className="min-w-0 truncate text-[12.5px]">
              <Link
                to="/projects/$id"
                params={{ id: h.projectId }}
                className="text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
              >
                {usedByLabel(h, envNames.get(h.environmentId))}
              </Link>
            </span>
            <span
              role="cell"
              className="truncate text-[12.5px] text-foreground/80"
            >
              {runsOnLabel(h)}
            </span>
            <RouteStates status={h.status} />
          </div>
        ))}
      </div>
    </div>
  )
}
