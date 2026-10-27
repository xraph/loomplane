// One domain on the Domains page: a header that opens to the hostnames
// every project points at it, or to the TXT record it still needs.
import { ChevronRightIcon } from "lucide-react"
import { useMemo, useState } from "react"

import { useEnvironmentsList } from "@/client/forge/loomplane/hooks"
import type {
  HostnameUse,
  Registration,
  ZoneOverview,
} from "@/client/forge/loomplane/types"
import { useQueries } from "@/client/queries"
import { ChallengeCredentialDialog } from "@/components/domains/challenge-credential-dialog"
import {
  DomainStatePill,
  RouteStates,
} from "@/components/domains/domain-states"
import {
  ManagedProof,
  RenewProof,
  TxtInstructions,
} from "@/components/domains/txt-instructions"
import { ZoneGrants } from "@/components/domains/zone-grants"
import { Button } from "@/components/ui/button"
import {
  cloudflareRegistrations,
  type PillTone,
  providerLabel,
  registrationLine,
  registrationStatusLine,
  runsOnLabel,
  usedByLabel,
  zoneDNSLabel,
  zoneDNSTone,
} from "@/lib/domains"
import { cn } from "@/lib/utils"

// The mock's columns: Hostname, Used by, Runs on, DNS, Certificate, Route.
const columns =
  "grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_170px_96px_140px_140px] gap-3"

export function DomainCard({
  zone,
  registration,
  expanded,
  onToggle,
}: {
  zone: ZoneOverview
  registration?: Registration
  expanded: boolean
  onToggle: () => void
}) {
  const z = zone.zone
  const name = z?.name ?? ""
  // Managed zones wait on the same TXT proof as external ones, though
  // Loomplane publishes theirs.
  const verifying = zone.state !== "ready" && !!z?.proofName
  // A zone verified before TXT proofs were required has none to publish.
  const unproven = zone.state !== "ready" && !!z && !z.proofName
  const [settingUp, setSettingUp] = useState(false)
  // Wildcards need a second, zone-scoped token on a Cloudflare zone, and
  // that token reaches all of its Cloudflare zone, so the domain must be one.
  const needsChallenge =
    zone.state === "ready" &&
    z?.mode === "managed" &&
    z.provider === "cloudflare" &&
    z.providerZone?.name === z.name &&
    !z.challengeConnectionId
  return (
    <section
      aria-label={name}
      className="overflow-hidden rounded-[14px] ring-1 ring-border"
    >
      <div className="relative flex flex-wrap items-center gap-x-3.5 gap-y-2 bg-subtle px-4 py-3">
        <button
          type="button"
          aria-expanded={expanded}
          onClick={onToggle}
          // The whole header opens the card; the grants control sits above.
          className="flex flex-[1_1_260px] flex-wrap items-center gap-x-3.5 gap-y-2 text-left outline-none after:absolute after:inset-0 focus-visible:after:rounded-[14px] focus-visible:after:ring-3 focus-visible:after:ring-ring/50"
        >
          <span className="flex flex-[1_1_260px] flex-col gap-[3px]">
            <span className="font-mono text-sm font-medium">{name}</span>
            <span className="text-[12.5px] text-muted-foreground">
              {registrationLine(zone, registration)}
            </span>
          </span>
          <span className="rounded-md bg-muted px-2 py-px text-xs font-medium text-foreground/80">
            {providerLabel(zone)}
          </span>
          <DomainStatePill tone={zoneDNSTone(zone)}>
            {zoneDNSLabel(zone)}
          </DomainStatePill>
        </button>
        {z && <ZoneGrants zone={z} />}
        <ChevronRightIcon
          aria-hidden="true"
          className={cn(
            "size-[15px] text-muted-foreground transition-transform",
            expanded && "rotate-90"
          )}
        />
      </div>
      {expanded && (
        <div className="border-t border-border/70">
          {zone.hostnames.length > 0 && (
            <HostnameTable hostnames={zone.hostnames} />
          )}
          {verifying &&
            z &&
            (z.mode === "managed" ? (
              <ManagedProof zone={z} />
            ) : (
              <TxtInstructions zone={z} />
            ))}
          {unproven && z && (
            // Not in the mock: zones there were all verified by TXT record.
            <div className="flex flex-col gap-2.5 px-4 py-3">
              <p className="text-[13px] leading-normal text-foreground/80">
                Loomplane now checks a TXT record before projects can use a
                domain, and this one doesn&apos;t have one yet. Renew its proof
                to get the record to add.
              </p>
              <RenewProof zone={z} />
            </div>
          )}
          {zone.hostnames.length === 0 && !verifying && !unproven && (
            // Not in the mock: a verified domain nothing points at yet.
            <p className="px-4 py-3 text-[12.5px] text-muted-foreground">
              No hostnames use this domain yet. Grant it to a project, then add
              a hostname from that project.
            </p>
          )}
          {needsChallenge && (
            // Not in the mock: the mock's *.acme.dev row assumes this is done.
            <div className="border-t border-border/70 px-2.5 py-1.5">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setSettingUp(true)}
              >
                Set up wildcard certificates
              </Button>
            </div>
          )}
        </div>
      )}
      {z && needsChallenge && (
        <ChallengeCredentialDialog
          zone={z}
          open={settingUp}
          onOpenChange={setSettingUp}
        />
      )}
    </section>
  )
}

function HostnameTable({ hostnames }: { hostnames: HostnameUse[] }) {
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
  return (
    <div className="overflow-x-auto">
      <div role="table" aria-label="Hostnames" className="min-w-[860px]">
        <div
          role="row"
          className={cn(
            columns,
            "border-b border-border/70 px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
          )}
        >
          {[
            "Hostname",
            "Used by",
            "Runs on",
            "DNS",
            "Certificate",
            "Route",
          ].map((h) => (
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
              columns,
              "items-center px-4 py-2.5 transition-colors hover:bg-subtle",
              i > 0 && "border-t border-border/70"
            )}
          >
            <span
              role="cell"
              className="truncate font-mono text-[12.5px] font-medium"
            >
              {h.hostname}
            </span>
            <span
              role="cell"
              className="truncate text-[12.5px] text-foreground/80"
            >
              {usedByLabel(h, envNames.get(h.environmentId))}
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
