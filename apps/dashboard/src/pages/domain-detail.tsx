// One domain's page: where it stands, the subdomains under it, every
// hostname that uses it, who may use it, and its wildcards and
// registration. Not in the mock, which has the Domains list only; it is
// built from the list's pieces and design language.
import { EllipsisIcon, GlobeIcon, PlusIcon } from "lucide-react"
import { type ReactNode, useEffect, useId, useMemo, useState } from "react"
import { toast } from "sonner"

import { statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDnsZonesGet,
} from "@/client/forge/loomplane/hooks"
import type {
  DomainDetail,
  HostnameUse,
  Registration,
  Zone,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { AddSubdomainDialog } from "@/components/domains/add-subdomain-dialog"
import { ChallengeCredentialDialog } from "@/components/domains/challenge-credential-dialog"
import { DomainStatePill } from "@/components/domains/domain-states"
import { DomainStatus } from "@/components/domains/domain-status"
import { HostnameTable } from "@/components/domains/hostname-table"
import { RemoveDomainDialog } from "@/components/domains/remove-domain-dialog"
import { useAutoRenew } from "@/components/domains/renewal-banner"
import { Restricted } from "@/components/domains/restricted"
import { SubdomainTable } from "@/components/domains/subdomain-table"
import {
  proofExpired,
  useCheckAgain,
} from "@/components/domains/txt-instructions"
import { ZoneGrants } from "@/components/domains/zone-grants"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { cloudLabel } from "@/lib/clouds"
import {
  cloudflareRegistrations,
  formatDay,
  formatPrice,
  isSet,
  providerLabel,
  registrationStatusLine,
  settling,
  useClock,
  zoneDNSLabel,
  zoneDNSTone,
} from "@/lib/domains"
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"

export function DomainDetailPage({ zoneId }: { zoneId: string }) {
  const { session, can } = useOrganization()
  // Without access nothing about domains is read.
  if (!can("organization_cloud:manage"))
    return (
      <Frame>
        <Restricted />
      </Frame>
    )
  // The route passes no ID for one too long to be a zone's.
  if (!zoneId)
    return (
      <Frame>
        <NotFound />
      </Frame>
    )
  return <Detail key={`${session.orgId}:${zoneId}`} zoneId={zoneId} />
}

function Frame({ name, children }: { name?: string; children: ReactNode }) {
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Domains", to: "/domains" },
          { label: name ?? "Domain" },
        ]}
      />
      <Page className="max-w-[1180px] gap-4">{children}</Page>
    </>
  )
}

function NotFound() {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <GlobeIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>
          This domain isn&apos;t in your organization
        </ZeroStateTitle>
        {/* Not in the mock. */}
        <ZeroStateDescription>
          It may have been removed, or the link is for another organization.
        </ZeroStateDescription>
      </ZeroStateHeader>
      <ZeroStateContent>
        <Link to="/domains" className={buttonVariants({ variant: "outline" })}>
          All domains
        </Link>
      </ZeroStateContent>
    </ZeroState>
  )
}

function Detail({ zoneId }: { zoneId: string }) {
  const detail = useQuery(useDnsZonesGet, { path: { zoneId } })
  const data = detail.data
  const status = detail.status === "error" ? statusOf(detail.error) : 0

  const { refetch } = detail
  const moving = !!data && settling([data.domain, ...data.subdomains], [])
  // Domain states have no realtime events; read them again while they move.
  useEffect(() => {
    if (!moving) return
    const timer = window.setInterval(() => {
      void refetch().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [moving, refetch])

  if (status === 403)
    return (
      <Frame>
        <Restricted />
      </Frame>
    )
  if (status === 404)
    return (
      <Frame>
        <NotFound />
      </Frame>
    )
  return (
    <Frame name={data?.domain.zone?.name}>
      {/* A refresh that fails keeps what was read before on screen. */}
      {detail.status === "error" && (
        <QueryError
          error={detail.error}
          fallback="This domain couldn't be loaded."
          retry={detail.refetch}
        />
      )}
      {data?.domain.zone ? (
        <Sections detail={data} zone={data.domain.zone} />
      ) : (
        detail.status !== "error" && (
          <div aria-busy="true" className="grid gap-3">
            <Skeleton className="h-12 w-72 rounded-[10px]" />
            <Skeleton className="h-32 rounded-[14px]" />
            <Skeleton className="h-48 rounded-[14px]" />
          </div>
        )
      )}
    </Frame>
  )
}

function Sections({ detail, zone }: { detail: DomainDetail; zone: Zone }) {
  const { domain, subdomains } = detail
  const navigate = useNavigate()
  const now = useClock()
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState(false)
  // By ID, so the dialog follows the subdomain as the page reads it again.
  const [removingID, setRemovingID] = useState("")
  const removingSub = subdomains.find((s) => s.zone?.id === removingID)
  const kind = domain.parentZoneId ? "subdomain" : "domain"
  const ready = domain.state === "ready"
  // A subdomain without a proof of its own is checked on the zone whose is.
  const proving = detail.proofZone?.zone ?? zone
  // Checking needs a value that can still verify.
  const checkable = !ready && !!proving.proofName && !proofExpired(proving, now)
  // A domain, or any zone with zones under it, lists its subdomains.
  const listsSubdomains = !domain.parentZoneId || subdomains.length > 0
  const hostnames = useMemo(
    () => [domain, ...subdomains].flatMap((z) => z.hostnames),
    [domain, subdomains]
  )
  const zoneOf = useMemo(() => {
    const names = new Map<string, string>()
    for (const z of [domain, ...subdomains])
      for (const h of z.hostnames) names.set(h.routeId, z.zone?.name ?? "")
    return (h: HostnameUse) => names.get(h.routeId) ?? ""
  }, [domain, subdomains])
  // Wildcards need a zone-scoped Cloudflare token, and that token reaches
  // all of its Cloudflare zone, so the domain must be one.
  const wildcards =
    zone.mode === "managed" &&
    zone.provider === "cloudflare" &&
    zone.providerZone.name === zone.name

  return (
    <>
      <PageHeading
        title={<span className="font-mono break-all">{zone.name}</span>}
        actions={
          <div
            role="group"
            aria-label="Domain actions"
            className="flex flex-wrap items-center gap-2"
          >
            {checkable && <CheckAgainAction zone={proving} />}
            <Button variant="outline" onClick={() => setAdding(true)}>
              <PlusIcon />
              Add subdomain
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="outline"
                    size="icon"
                    aria-label="More domain actions"
                  />
                }
              >
                <EllipsisIcon />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-48">
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => setRemoving(true)}
                >
                  Remove {kind}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        }
      >
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <DomainStatePill tone={zoneDNSTone(domain)}>
            {zoneDNSLabel(domain)}
          </DomainStatePill>
          <span className="rounded-md bg-muted px-2 py-px text-xs font-medium whitespace-nowrap text-foreground/80">
            {providerLabel(domain)}
          </span>
        </div>
      </PageHeading>

      <Section title="Status">
        <DomainStatus detail={detail} />
      </Section>

      {listsSubdomains && (
        <Section
          title="Subdomains"
          count={subdomains.length}
          action={
            <Button variant="ghost" size="sm" onClick={() => setAdding(true)}>
              <PlusIcon />
              Add subdomain
            </Button>
          }
        >
          {subdomains.length > 0 ? (
            <SubdomainTable
              detail={detail}
              onRemove={(sub) => setRemovingID(sub.zone?.id ?? "")}
            />
          ) : (
            // Not in the mock.
            <p className="px-4 py-3 text-[12.5px] text-muted-foreground">
              No subdomains yet.
            </p>
          )}
        </Section>
      )}

      <Section title="Hostnames" count={hostnames.length}>
        {hostnames.length > 0 ? (
          <HostnameTable
            hostnames={hostnames}
            subdomain={subdomains.length > 0 ? zoneOf : undefined}
          />
        ) : (
          // Not in the mock: a domain nothing points at yet.
          <p className="px-4 py-3 text-[12.5px] text-muted-foreground">
            No hostnames use this domain yet. Grant it to a project, then add a
            hostname from that project.
          </p>
        )}
      </Section>

      <Section title="Access">
        <div className="flex flex-col gap-2.5 px-4 py-3">
          <ZoneGrants zone={zone} inline />
          {/* Not in the mock. */}
          <p className="text-[12.5px] text-muted-foreground">
            Subdomains have their own projects.
          </p>
        </div>
      </Section>

      {/* Task 8 lists the records Loomplane wrote here. */}
      <Section title="DNS records" />

      {wildcards && (
        <Section title="Wildcards">
          <Wildcards zone={zone} ready={ready} />
        </Section>
      )}

      {detail.registration && (
        <Section title="Registration">
          <RegistrationDetails registration={detail.registration} />
        </Section>
      )}

      <DangerZone kind={kind} onRemove={() => setRemoving(true)} />

      <AddSubdomainDialog
        domain={zone}
        open={adding}
        onOpenChange={setAdding}
        onAdded={(added) =>
          void navigate({
            to: "/domains/$zoneId",
            params: { zoneId: added.id },
          })
        }
      />
      <RemoveDomainDialog
        detail={detail}
        open={removing}
        onOpenChange={setRemoving}
        onRemoved={() =>
          // A subdomain's page goes back to the zone it was part of.
          void (detail.parent?.zone
            ? navigate({
                to: "/domains/$zoneId",
                params: { zoneId: detail.parent.zone.id },
                replace: true,
              })
            : navigate({ to: "/domains", replace: true }))
        }
      />
      {removingSub && (
        <RemoveDomainDialog
          detail={detail}
          zone={removingSub}
          open
          onOpenChange={(next) => {
            if (!next) setRemovingID("")
          }}
          onRemoved={() => setRemovingID("")}
        />
      )}
    </>
  )
}

function Section({
  title,
  count,
  action,
  children,
}: {
  title: string
  count?: number
  /** Sits at the end of the section's heading row. */
  action?: ReactNode
  children?: ReactNode
}) {
  const id = useId()
  return (
    <section
      aria-labelledby={id}
      className="overflow-hidden rounded-[14px] ring-1 ring-border"
    >
      <div
        className={
          children
            ? "flex items-center gap-2 border-b border-border/70 bg-subtle px-4 py-2.5"
            : "flex items-center gap-2 bg-subtle px-4 py-2.5"
        }
      >
        <h2 id={id} className="text-[13px] font-semibold">
          {title}
        </h2>
        {count !== undefined && (
          <span className="font-mono text-[11px] text-muted-foreground">
            {count}
          </span>
        )}
        {action && <div className="-my-1 ml-auto">{action}</div>}
      </div>
      {children}
    </section>
  )
}

// The DeleteProject pattern. Not in the mock.
function DangerZone({
  kind,
  onRemove,
}: {
  kind: "domain" | "subdomain"
  onRemove: () => void
}) {
  const id = useId()
  return (
    <section
      aria-labelledby={id}
      className="flex flex-wrap items-center gap-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-destructive/25"
    >
      <div className="grid min-w-0 flex-[1_1_360px] gap-1">
        <h2 id={id} className="text-[14px] font-semibold">
          Remove {kind}
        </h2>
        <p className="text-[12.5px] leading-normal text-muted-foreground">
          {kind === "domain"
            ? "Removes the domain and its subdomains from Loomplane. Hostnames that use them have to go first."
            : "Removes the subdomain, and any zone under it, from Loomplane. Hostnames that use them have to go first."}{" "}
          Loomplane deletes only the DNS records it wrote.
        </p>
      </div>
      <Button
        variant="outline"
        className="border-destructive/25 text-destructive hover:bg-destructive/10 hover:text-destructive"
        onClick={onRemove}
      >
        Remove {kind}
      </Button>
    </section>
  )
}

// The header's check, on the zone whose record decides this one. Not in
// the mock: what it found shows as a toast.
function CheckAgainAction({ zone }: { zone: Zone }) {
  const { check, pending } = useCheckAgain(zone)
  const run = async () => {
    const outcome = await check()
    if (outcome.kind === "verified") toast.success(`${zone.name} is verified.`)
    else if (outcome.kind === "missing")
      toast(`The TXT record for ${zone.name} isn't visible yet.`)
    else toast.error(outcome.message)
  }
  return (
    <Button variant="outline" disabled={pending} onClick={() => void run()}>
      Check again
    </Button>
  )
}

// Not in the mock, whose *.acme.dev row assumes the token is already there.
function Wildcards({ zone, ready }: { zone: Zone; ready: boolean }) {
  const [settingUp, setSettingUp] = useState(false)
  const connections = useQuery(useConnectionsList)
  if (zone.challengeConnectionId) {
    const token = connections.data?.connections.find(
      (c) => c?.id === zone.challengeConnectionId
    )
    return (
      <p className="px-4 py-3 text-[13px] leading-normal text-foreground/80">
        Wildcard certificates are set up with{" "}
        {token?.name ?? "a second Cloudflare token"}.
      </p>
    )
  }
  if (!ready)
    return (
      <p className="px-4 py-3 text-[13px] leading-normal text-muted-foreground">
        Once the domain is verified, you can set up wildcard certificates here.
      </p>
    )
  return (
    <div className="flex flex-col items-start gap-2.5 px-4 py-3">
      <p className="text-[13px] leading-normal text-foreground/80">
        Wildcard certificates need a second Cloudflare token with DNS Edit on{" "}
        {zone.name} only.
      </p>
      <Button variant="outline" size="sm" onClick={() => setSettingUp(true)}>
        Set up wildcard certificates
      </Button>
      <ChallengeCredentialDialog
        zone={zone}
        open={settingUp}
        onOpenChange={setSettingUp}
      />
    </div>
  )
}

function RegistrationDetails({ registration }: { registration: Registration }) {
  const {
    registration: r,
    flip,
    pending,
    error,
    label,
  } = useAutoRenew(registration)
  const price = formatPrice(r.quote.currency, r.quote.renewalCost)
  const done = r.status === "succeeded"
  return (
    <div className="flex flex-col gap-2.5 px-4 py-3">
      {!done && (
        <p className="text-[13px] leading-normal text-foreground/80">
          {registrationStatusLine(r)}
        </p>
      )}
      <dl className="grid grid-cols-[minmax(0,120px)_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-[13px]">
        <dt className="text-muted-foreground">Registrar</dt>
        <dd>{cloudLabel(r.provider || "cloudflare")}</dd>
        {r.accountId && (
          <>
            <dt className="text-muted-foreground">Account</dt>
            <dd className="min-w-0 break-words">{r.accountId}</dd>
          </>
        )}
        {isSet(r.domainExpiresAt) && (
          <>
            <dt className="text-muted-foreground">
              {r.autoRenew ? "Renews" : "Expires"}
            </dt>
            <dd>{formatDay(r.domainExpiresAt)}</dd>
          </>
        )}
        {price && (
          <>
            <dt className="text-muted-foreground">Renewal price</dt>
            <dd>{price}</dd>
          </>
        )}
        {done && (
          <>
            <dt className="text-muted-foreground">Auto-renew</dt>
            <dd>{r.autoRenew ? "On" : "Off"}</dd>
          </>
        )}
      </dl>
      {error && (
        <p role="alert" className="text-[12.5px] text-destructive">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {done && (
          <Button
            variant="outline"
            size="sm"
            disabled={pending}
            onClick={() => void flip()}
          >
            {label}
          </Button>
        )}
        <a
          href={cloudflareRegistrations(r.accountId)}
          target="_blank"
          rel="noreferrer"
          className={buttonVariants({ variant: "ghost", size: "sm" })}
        >
          Open in Cloudflare
        </a>
      </div>
    </div>
  )
}
