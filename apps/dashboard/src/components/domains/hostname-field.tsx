// Picking a hostname for a route: one of the project's verified domains and
// a name under it. Not in the mock, which only shows hostnames once they
// exist.
import { useEffect, useId } from "react"

import { statusOf } from "@/client/errors"
import { useDomainsProject } from "@/client/forge/loomplane/hooks"
import type { ProjectDomain } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { usedByLabel } from "@/lib/domains"
import { cn } from "@/lib/utils"

export type HostnameValue = { zoneId: string; hostname: string }

const field =
  "flex flex-col gap-[5px] text-[12.5px] font-medium text-foreground/80"
const control =
  "h-9 rounded-[9px] border border-input bg-background px-2.5 font-mono text-[13px] font-normal text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"

// The label under zone that hostname names; empty for the apex.
function labelOf(hostname: string, zone: string): string {
  return hostname.endsWith(`.${zone}`)
    ? hostname.slice(0, -(zone.length + 1))
    : ""
}

function hostnameOf(label: string, zone: string): string {
  const name = label.trim().toLowerCase()
  return name ? `${name}.${zone}` : zone
}

export function HostnameField({
  projectId,
  value,
  onChange,
  allowWildcard = true,
}: {
  projectId: string
  value: HostnameValue
  onChange(v: HostnameValue): void
  allowWildcard?: boolean
}) {
  const ids = useId()
  const domains = useQuery(useDomainsProject, { path: { projectId } })
  const verified = (domains.data?.domains ?? []).filter((d) => d.verified)
  const zone = verified.find((d) => d.zoneId === value.zoneId)
  const first = verified[0]

  // With nothing picked yet, start at the first domain's apex.
  useEffect(() => {
    if (!value.zoneId && first)
      onChange({ zoneId: first.zoneId, hostname: first.name })
  }, [value.zoneId, first, onChange])

  if (domains.status === "error") {
    return statusOf(domains.error) === 403 ? (
      <p className="text-[12.5px] text-muted-foreground">
        You can&apos;t see this project&apos;s domains.
      </p>
    ) : (
      <QueryError
        error={domains.error}
        fallback="This project's domains couldn't be loaded."
        retry={domains.refetch}
      />
    )
  }
  if (!domains.data) return <Skeleton className="h-9 rounded-[9px]" />
  if (verified.length === 0) {
    return (
      <p className="text-[12.5px] text-muted-foreground">
        No verified domain is granted to this project yet. Grant one from the
        Domains page.
      </p>
    )
  }

  const label = zone ? labelOf(value.hostname, zone.name) : ""
  const wildcards = allowWildcard && !!zone?.wildcards
  const pickZone = (next: ProjectDomain) => {
    // A wildcard stays only where the new domain can serve one.
    const keep =
      label === "*" && !(allowWildcard && next.wildcards) ? "" : label
    onChange({ zoneId: next.zoneId, hostname: hostnameOf(keep, next.name) })
  }
  const inUse = zone?.hostnames.find(
    (h) => h.hostname.toLowerCase() === value.hostname.toLowerCase()
  )

  return (
    <div role="group" aria-label="Hostname" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-end gap-2">
        <label
          htmlFor={`${ids}-label`}
          className={cn(field, "min-w-[140px] flex-[1_1_160px]")}
        >
          Subdomain
          <input
            id={`${ids}-label`}
            type="text"
            value={label}
            onChange={(e) =>
              zone &&
              onChange({
                zoneId: zone.zoneId,
                hostname: hostnameOf(e.target.value, zone.name),
              })
            }
            placeholder="Empty for the apex"
            autoComplete="off"
            spellCheck={false}
            className={control}
          />
        </label>
        <span aria-hidden="true" className="pb-2 font-mono text-[13px]">
          .
        </span>
        <label
          htmlFor={`${ids}-zone`}
          className={cn(field, "min-w-[160px] flex-[1_1_200px]")}
        >
          Domain
          <select
            id={`${ids}-zone`}
            value={zone?.zoneId ?? ""}
            onChange={(e) => {
              const next = verified.find((d) => d.zoneId === e.target.value)
              if (next) pickZone(next)
            }}
            className={control}
          >
            {verified.map((d) => (
              <option key={d.zoneId} value={d.zoneId}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
        {wildcards && zone && (
          <Button
            type="button"
            variant="outline"
            aria-pressed={label === "*"}
            onClick={() =>
              onChange({
                zoneId: zone.zoneId,
                hostname: hostnameOf(label === "*" ? "" : "*", zone.name),
              })
            }
          >
            Every subdomain
          </Button>
        )}
      </div>
      <div className="flex flex-col gap-1 text-[12.5px] leading-normal text-muted-foreground">
        <span>
          Hostname{" "}
          <span
            data-testid="resulting-hostname"
            className="font-mono text-foreground"
          >
            {value.hostname}
          </span>
        </span>
        {inUse && (
          <span className="text-destructive">
            Already in use by {usedByLabel(inUse)}
          </span>
        )}
        {zone && label.includes("*") && !wildcards && (
          <span className="text-destructive">
            {allowWildcard
              ? `Wildcards on ${zone.name} need wildcard certificates, which are set up on the domain's card on the Domains page.`
              : "This hostname can't be a wildcard."}
          </span>
        )}
      </div>
    </div>
  )
}
