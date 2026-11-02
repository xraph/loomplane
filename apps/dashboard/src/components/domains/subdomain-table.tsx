// The zones under a domain on its page: where each one's DNS sits when
// that isn't the domain's, who may use it, how many hostnames it has and
// whether it's verified. Not in the mock, which has no subdomains.
import { useMemo } from "react"

import { useProjectsList } from "@/client/forge/loomplane/hooks"
import type { DomainDetail, ZoneOverview } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DomainStatePill } from "@/components/domains/domain-states"
import { Button } from "@/components/ui/button"
import {
  grantsLine,
  plural,
  providerLabel,
  sameHosting,
  zoneDNSLabel,
  zoneDNSTone,
} from "@/lib/domains"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

const columns =
  "grid grid-cols-[minmax(0,1.3fr)_120px_minmax(0,1.2fr)_100px_170px_120px] gap-3"

export function SubdomainTable({
  detail,
  onRemove,
}: {
  detail: DomainDetail
  /** Offers Remove on each row. */
  onRemove?: (subdomain: ZoneOverview) => void
}) {
  const projects = useQuery(useProjectsList)
  const names = useMemo(
    () =>
      new Map(
        (projects.data?.projects ?? []).map((p) => [p.id, p.name] as const)
      ),
    [projects.data]
  )
  const domain = detail.domain.zone
  return (
    <div className="overflow-x-auto">
      <div role="table" aria-label="Subdomains" className="min-w-[820px]">
        <div
          role="row"
          className={cn(
            columns,
            "border-b border-border/70 px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
          )}
        >
          {["Name", "Hosting", "Projects", "Hostnames", "State"].map((h) => (
            <span key={h} role="columnheader">
              {h}
            </span>
          ))}
          <span role="columnheader" className="sr-only">
            Actions
          </span>
        </div>
        {detail.subdomains.map((sub, i) => {
          const z = sub.zone
          // A subdomain's DNS is its domain's unless it's delegated elsewhere.
          const own = !!z && !!domain && !sameHosting(z, domain)
          return (
            <div
              key={z?.id ?? i}
              role="row"
              className={cn(
                columns,
                "items-center px-4 py-2.5 transition-colors hover:bg-subtle",
                i > 0 && "border-t border-border/70"
              )}
            >
              <span
                role="cell"
                className="truncate font-mono text-[12.5px] font-medium"
              >
                {z?.name}
              </span>
              <span role="cell" className="min-w-0">
                {own && (
                  <span className="rounded-md bg-muted px-2 py-px text-xs font-medium whitespace-nowrap text-foreground/80">
                    {providerLabel(sub)}
                  </span>
                )}
              </span>
              <span
                role="cell"
                className="truncate text-[12.5px] text-muted-foreground"
              >
                {grantsLine(z?.projectIds ?? [], names)}
              </span>
              <span
                role="cell"
                className="text-[12.5px] whitespace-nowrap text-muted-foreground"
              >
                {plural(sub.hostnames.length, "hostname")}
              </span>
              <span role="cell" className="min-w-0">
                <DomainStatePill tone={zoneDNSTone(sub)}>
                  {zoneDNSLabel(sub)}
                </DomainStatePill>
              </span>
              <span role="cell" className="flex justify-end gap-1">
                <Button
                  variant="ghost"
                  size="sm"
                  nativeButton={false}
                  render={
                    <Link
                      to="/domains/$zoneId"
                      params={{ zoneId: z?.id ?? "" }}
                    />
                  }
                >
                  Open
                </Button>
                {onRemove && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-destructive hover:text-destructive"
                    onClick={() => onRemove(sub)}
                  >
                    Remove
                  </Button>
                )}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
