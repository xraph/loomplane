// One domain on the Domains page: a header that opens the domain's page,
// with its subdomains listed under it, each opening its own.
import { ChevronRightIcon } from "lucide-react"
import { useMemo } from "react"

import { useProjectsList } from "@/client/forge/loomplane/hooks"
import type { Registration, ZoneOverview } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DomainStatePill } from "@/components/domains/domain-states"
import {
  cloudflareRegistrations,
  type DomainGroup,
  domainRollup,
  grantsLine,
  type PillTone,
  plural,
  providerLabel,
  registrationLine,
  registrationStatusLine,
  sameHosting,
  zoneDNSLabel,
  zoneDNSTone,
} from "@/lib/domains"
import { Link } from "@/lib/router"

const chip =
  "rounded-md bg-muted px-2 py-px text-xs font-medium whitespace-nowrap text-foreground/80"

export function DomainCard({
  group,
  registration,
  now,
}: {
  group: DomainGroup
  registration?: Registration
  /** The page's clock, for proofs that expire. */
  now: number
}) {
  const { domain, subdomains } = group
  const name = domain.zone?.name ?? ""
  const rollup = domainRollup(group, registration, now)
  // Grants name projects; the overview carries their IDs.
  const projects = useQuery(useProjectsList)
  const names = useMemo(
    () =>
      new Map(
        (projects.data?.projects ?? []).map((p) => [p.id, p.name] as const)
      ),
    [projects.data]
  )
  return (
    <section
      aria-label={name}
      className="overflow-hidden rounded-[14px] ring-1 ring-border"
    >
      <Link
        to="/domains/$zoneId"
        params={{ zoneId: domain.zone?.id ?? "" }}
        className="group flex flex-wrap items-center gap-x-3.5 gap-y-2 bg-subtle px-4 py-3 outline-none hover:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset"
      >
        <span className="flex min-w-0 flex-[1_1_260px] flex-col gap-[3px]">
          <span className="truncate font-mono text-sm font-medium">{name}</span>
          <span className="text-[12.5px] text-muted-foreground">
            {registrationLine(domain, registration)}
          </span>
        </span>
        <span className={chip}>{providerLabel(domain)}</span>
        <DomainStatePill tone={rollup.tone}>
          {rollup.label}
          {rollup.count > 0 && (
            <>
              {" "}
              <span className="font-mono">{rollup.count}</span>
            </>
          )}
        </DomainStatePill>
        <span className="min-w-[150px] text-[12.5px] text-muted-foreground">
          {grantsLine(domain.zone?.projectIds ?? [], names)}
        </span>
        <ChevronRightIcon
          aria-hidden="true"
          className="size-[15px] text-muted-foreground transition-transform group-hover:translate-x-0.5"
        />
      </Link>
      {subdomains.length > 0 && (
        // Not in the mock, which has no subdomains.
        <ul aria-label="Subdomains" className="border-t border-border/70">
          {subdomains.map((sub, i) => (
            <li
              key={sub.zone?.id ?? i}
              className={i > 0 ? "border-t border-border/70" : undefined}
            >
              <SubdomainRow domain={domain} subdomain={sub} names={names} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function SubdomainRow({
  domain,
  subdomain,
  names,
}: {
  domain: ZoneOverview
  subdomain: ZoneOverview
  names: Map<string, string>
}) {
  const z = subdomain.zone
  // A subdomain's DNS is its domain's unless it's delegated elsewhere.
  const ownHosting = !!z && !!domain.zone && !sameHosting(z, domain.zone)
  return (
    <Link
      to="/domains/$zoneId"
      params={{ zoneId: z?.id ?? "" }}
      className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 py-2.5 pr-4 pl-7 outline-none hover:bg-subtle focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset"
    >
      <span className="min-w-0 flex-[1_1_220px] truncate font-mono text-[12.5px] font-medium">
        {z?.name}
      </span>
      {ownHosting && <span className={chip}>{providerLabel(subdomain)}</span>}
      <span className="min-w-[150px] text-[12.5px] text-muted-foreground">
        {grantsLine(z?.projectIds ?? [], names)}
      </span>
      <span className="text-[12.5px] whitespace-nowrap text-muted-foreground">
        {plural(subdomain.hostnames.length, "hostname")}
      </span>
      <DomainStatePill tone={zoneDNSTone(subdomain)}>
        {zoneDNSLabel(subdomain)}
      </DomainStatePill>
    </Link>
  )
}

const registrationPills: Record<string, [string, PillTone]> = {
  approved: ["Registering", "warn"],
  registering: ["Registering", "warn"],
  zone_pending: ["Activating zone", "warn"],
  failed: ["Not registered", "failed"],
  price_changed: ["Not registered", "failed"],
  action_required: ["Needs you", "warn"],
  blocked: ["Needs you", "warn"],
}

/**
 * RegistrationCard is a registration that has no zone yet: still being
 * bought, or ended without one. Not in the mock, which shows only zones;
 * the register dialog promises its outcome shows on this page.
 */
export function RegistrationCard({
  registration: r,
}: {
  registration: Registration
}) {
  const handOff = r.status === "action_required" || r.status === "blocked"
  const [pill, tone] = registrationPills[r.status] ?? ["Unconfirmed", "idle"]
  return (
    <section
      aria-label={r.name}
      className="overflow-hidden rounded-[14px] ring-1 ring-border"
    >
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2 bg-subtle px-4 py-3">
        <span className="flex flex-[1_1_260px] flex-col gap-[3px]">
          <span className="font-mono text-sm font-medium">{r.name}</span>
          <span className="text-[12.5px] text-muted-foreground">
            {registrationStatusLine(r)}
            {handOff && (
              <>
                {" "}
                <a
                  href={cloudflareRegistrations(r.accountId)}
                  target="_blank"
                  rel="noreferrer"
                  className="text-foreground underline underline-offset-4"
                >
                  Open Cloudflare
                </a>
              </>
            )}
          </span>
        </span>
        <span className="rounded-md bg-muted px-2 py-px text-xs font-medium text-foreground/80">
          Cloudflare
        </span>
        <DomainStatePill tone={tone}>{pill}</DomainStatePill>
      </div>
    </section>
  )
}
