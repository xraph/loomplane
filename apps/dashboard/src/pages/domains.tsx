// Domains: every zone the organization owns, the hostnames its projects
// point at each, and the domains registered through Loomplane with their
// renewals. Built to the redesign's Domains artboard.
import { GlobeIcon, LockIcon, PlusIcon } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useDomainRegistrationsList,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type {
  Registration,
  Zone,
  ZoneOverview,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { BringDomainPanel } from "@/components/domains/bring-domain-panel"
import { DomainCard, RegistrationCard } from "@/components/domains/domain-card"
import { RegisterDomainDialog } from "@/components/domains/register-domain-dialog"
import { RenewalBanner } from "@/components/domains/renewal-banner"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import {
  endedWithoutZone,
  inFlight,
  isSet,
  renewsSoon,
  settling,
  useClock,
  zoneNeedsAttention,
} from "@/lib/domains"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

export type DomainsShow = "all" | "attention"

export type DomainsSearch = {
  /** The registration a notice links to; its banner shows. */
  registration?: string
  /** From the renewal notice: focus the auto-renew switch. */
  action?: "auto-renew-off"
  /** A zone to open, by ID or name. */
  zone?: string
  show?: DomainsShow
}

const description =
  "Domains your organization owns. Grant them to projects, then point hostnames at any service or gateway."

export function Domains({
  search = {},
  onSearch,
}: {
  search?: DomainsSearch
  /** Keeps the page's choices in the URL. Without it they're local. */
  onSearch?: (search: DomainsSearch) => void
}) {
  const { session, can } = useOrganization()
  // Without access nothing about domains is read.
  if (!can("organization_cloud:manage")) {
    return (
      <>
        <SiteHeader crumbs={[{ label: "Domains" }]} />
        <Page className="max-w-[1180px] gap-4">
          <PageHeading title="Domains" description={description} />
          <Restricted />
        </Page>
      </>
    )
  }
  return <DomainsList key={session.orgId} search={search} onSearch={onSearch} />
}

function Restricted() {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LockIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>Domain access is restricted</ZeroStateTitle>
        <ZeroStateDescription>
          Ask your organization owner for permission to manage Cloud
          infrastructure.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

function Empty({ title, children }: { title: string; children: string }) {
  return (
    <ZeroState>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <GlobeIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{children}</ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

type Item =
  | {
      kind: "zone"
      id: string
      zone: ZoneOverview
      registration?: Registration
    }
  | { kind: "registration"; id: string; registration: Registration }

function DomainsList({
  search,
  onSearch,
}: {
  search: DomainsSearch
  onSearch?: (search: DomainsSearch) => void
}) {
  const overview = useQuery(useDomainsOverview)
  const registrationList = useQuery(useDomainRegistrationsList)
  const now = useClock()
  const [bringing, setBringing] = useState(false)
  const [registering, setRegistering] = useState(false)
  const [localShow, setLocalShow] = useState<DomainsShow>("all")
  const show = onSearch ? (search.show ?? "all") : localShow
  // Cards opened or closed by hand; the rest follow the search params.
  const [toggled, setToggled] = useState<Record<string, boolean>>({})

  const zones = useMemo(() => overview.data?.zones ?? [], [overview.data])
  // The registrations list is the fresher read; the overview carries the
  // same rows while it loads.
  const registrations = useMemo(
    () =>
      ((registrationList.data ?? overview.data)?.registrations ?? []).filter(
        (r): r is Registration => !!r
      ),
    [registrationList.data, overview.data]
  )

  const { refetch: refetchOverview } = overview
  const { refetch: refetchRegistrations } = registrationList
  const moving = settling(zones, registrations)
  // Domain states have no realtime events; read them again while they move.
  useEffect(() => {
    if (!moving) return
    const timer = window.setInterval(() => {
      void refetchOverview().catch(() => undefined)
      void refetchRegistrations().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [moving, refetchOverview, refetchRegistrations])

  const items = useMemo<Item[]>(() => {
    const registrationOf = (z: ZoneOverview) => {
      const id = z.registrationId || z.zone?.registrationId
      return id ? registrations.find((r) => r.id === id) : undefined
    }
    const out: Item[] = zones.map((z) => ({
      kind: "zone",
      id: z.zone?.id ?? "",
      zone: z,
      registration: registrationOf(z),
    }))
    for (const r of registrations) {
      const unsettled =
        inFlight.includes(r.status) || endedWithoutZone.includes(r.status)
      const zoned = zones.some(
        (z) =>
          (!!r.zoneId && z.zone?.id === r.zoneId) || z.zone?.name === r.name
      )
      if (unsettled && !zoned)
        out.push({ kind: "registration", id: r.id, registration: r })
    }
    return out
  }, [zones, registrations])

  const attention = (item: Item) =>
    item.kind === "registration" ||
    zoneNeedsAttention(item.zone, item.registration)
  const shown = items.filter((i) => show === "all" || attention(i))
  const hostnames = zones.reduce((n, z) => n + z.hostnames.length, 0)
  const banners = registrations.filter(
    (r) =>
      r.status === "succeeded" &&
      isSet(r.domainExpiresAt) &&
      (renewsSoon(r, now) || r.id === search.registration)
  )

  const isOpen = (item: Item) =>
    toggled[item.id] ??
    (item.kind === "zone" &&
      ((!!search.zone &&
        (item.id === search.zone || item.zone.zone?.name === search.zone)) ||
        (!!search.registration &&
          item.registration?.id === search.registration)))

  const pick = (next: DomainsShow) => {
    if (!onSearch) {
      setLocalShow(next)
      return
    }
    const rest = { ...search }
    delete rest.show
    onSearch(next === "all" ? rest : { ...rest, show: next })
  }

  const added = (zone: Zone) => {
    setBringing(false)
    setToggled((t) => ({ ...t, [zone.id]: true }))
    void refetchOverview().catch(() => undefined)
  }

  const denied = overview.status === "error" && statusOf(overview.error) === 403
  return (
    <>
      <SiteHeader crumbs={[{ label: "Domains" }]} />
      <Page className="max-w-[1180px] gap-4">
        <PageHeading
          title="Domains"
          description={description}
          actions={
            !denied && (
              <>
                <Button
                  variant="outline"
                  aria-expanded={bringing}
                  onClick={() => setBringing((b) => !b)}
                >
                  Bring a domain
                </Button>
                <Button onClick={() => setRegistering(true)}>
                  <PlusIcon className="size-3.5" />
                  Register a domain
                </Button>
              </>
            )
          }
        />

        {bringing && !denied && <BringDomainPanel onAdded={added} />}
        <RegisterDomainDialog
          open={registering && !denied}
          onOpenChange={setRegistering}
        />

        {banners.map((r) => (
          <RenewalBanner
            key={r.id}
            registration={r}
            focus={
              r.id === search.registration && search.action === "auto-renew-off"
            }
          />
        ))}

        {/* A refresh that fails keeps what was read before on screen, with
            the failure above it, so open cards and popovers stay put. */}
        {!denied && overview.status === "error" && (
          <QueryError
            error={overview.error}
            fallback="Domains couldn't be loaded."
            retry={overview.refetch}
          />
        )}

        {denied ? (
          <Restricted />
        ) : !overview.data ? (
          overview.status === "error" ? null : (
            <div className="grid gap-3">
              <Skeleton className="h-9 w-64 rounded-[10px]" />
              <Skeleton className="h-16 rounded-[14px]" />
              <Skeleton className="h-16 rounded-[14px]" />
            </div>
          )
        ) : items.length === 0 ? (
          <Empty title="No domains yet">
            Bring a domain you own, or register a new one in your Cloudflare
            account.
          </Empty>
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2.5">
              <div
                role="radiogroup"
                aria-label="Show"
                className="flex gap-0.5 rounded-[10px] bg-muted p-[3px]"
              >
                {(
                  [
                    ["all", "All", items.length],
                    [
                      "attention",
                      "Needs attention",
                      items.filter(attention).length,
                    ],
                  ] as const
                ).map(([value, label, n]) => {
                  const on = value === show
                  return (
                    <button
                      key={value}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => pick(value)}
                      className={cn(
                        "flex h-7 items-center gap-1.5 rounded-lg px-[11px] text-[12.5px] font-medium",
                        on
                          ? "bg-background text-foreground shadow-xs"
                          : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      {label}{" "}
                      <span className="font-mono text-[11px] text-muted-foreground">
                        {n}
                      </span>
                    </button>
                  )
                })}
              </div>
              <span className="text-[12.5px] text-muted-foreground">
                {/* Not in the mock: pluralized. */}
                {plural(items.length, "domain")} ·{" "}
                {plural(hostnames, "hostname")} in use
              </span>
            </div>

            {shown.length === 0 ? (
              <Empty title="Nothing needs attention">
                Every domain is verified and every certificate is current.
              </Empty>
            ) : (
              shown.map((item) =>
                item.kind === "zone" ? (
                  <DomainCard
                    key={item.id}
                    zone={item.zone}
                    registration={item.registration}
                    expanded={isOpen(item)}
                    onToggle={() =>
                      setToggled((t) => ({ ...t, [item.id]: !isOpen(item) }))
                    }
                  />
                ) : (
                  <RegistrationCard
                    key={item.id}
                    registration={item.registration}
                  />
                )
              )
            )}
          </>
        )}

        <p className="text-xs leading-normal text-muted-foreground">
          Domains you register here are bought with your own Cloudflare account
          and billed to it. Loomplane never pays for or holds a domain.
        </p>
      </Page>
    </>
  )
}

// Not in the mock, whose sample only ever had several: "1 domain".
function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`
}
