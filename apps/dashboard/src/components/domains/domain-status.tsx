// Where a domain stands on its page: verified or what it still needs, who
// hosts its DNS, and for a subdomain, the domain it's part of. Not in the
// mock, which has no page per domain; the proof parts are the list's.
import type { ReactNode } from "react"

import { useConnectionsList } from "@/client/forge/loomplane/hooks"
import type { DomainDetail, Zone } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  CheckAgain,
  ManagedProof,
  ProofTable,
  RenewProof,
  TxtInstructions,
} from "@/components/domains/txt-instructions"
import { providerLabel } from "@/lib/domains"
import { Link } from "@/lib/router"

export function DomainStatus({ detail }: { detail: DomainDetail }) {
  const { domain, parent, proofZone } = detail
  const z = domain.zone
  const ready = domain.state === "ready"
  // Verified, but the record that proves it is gone from public DNS.
  const missing = domain.state === "proof_missing"
  // A subdomain without a proof of its own waits on the zone whose is.
  const proving = proofZone?.zone ?? z
  // The zone that proves a nested subdomain can be further up than its
  // parent, and the line names that one.
  const through = proofZone?.zone ?? parent?.zone
  // Waiting on another zone's proof, it can take one of its own instead.
  const waiting = !ready && !missing && !!domain.proofZoneId && !!through
  return (
    <div className="divide-y divide-border/70">
      {parent?.zone && (
        <div className="flex flex-col gap-2.5 px-4 py-3">
          <p className="text-[13px] leading-normal text-foreground/80">
            Part of <ZoneLink zone={parent.zone} />
            {domain.proofZoneId &&
              through &&
              (ready || missing ? (
                through.id === parent.zone.id ? (
                  " · verified through it"
                ) : (
                  <>
                    {" "}
                    · verified through <ZoneLink zone={through} />
                  </>
                )
              ) : (
                <>
                  {" "}
                  · waits for <ZoneLink zone={through} />
                  &apos;s verification
                </>
              ))}
          </p>
          {/* not in the mock */}
          {waiting && z && (
            <RenewProof zone={z} label="Verify it on its own instead" />
          )}
        </div>
      )}
      {missing
        ? proving && (
            <MissingProof
              zone={proving}
              through={proofZone?.zone ? proofZone.zone.name : ""}
            />
          )
        : ready
          ? // Inherited verification is said above.
            !domain.proofZoneId && (
              <div className="flex flex-col gap-2.5 px-4 py-3">
                <p className="text-[13px] leading-normal text-foreground/80">
                  Verified. Projects you grant it to can point hostnames at it.
                </p>
                {/* Not in the mock: a record deleted since is published again. */}
                {z?.proofName && <CheckAgain zone={z} />}
              </div>
            )
          : proving && (
              <div>
                {proofZone?.zone && (
                  <p className="px-4 pt-3 text-[13px] leading-normal text-foreground/80">
                    This is {proofZone.zone.name}&apos;s record.
                  </p>
                )}
                <Proof zone={proving} />
              </div>
            )}
      {z && <Hosting zone={z} label={providerLabel(domain)} />}
    </div>
  )
}

// Not in the mock: a verified zone whose record a check found gone. Its
// value still counts, so it's published or added again, never renewed.
function MissingProof({ zone, through }: { zone: Zone; through: string }) {
  return (
    <div className="flex flex-col gap-2.5 px-4 py-3">
      <p className="text-[13px] leading-normal text-foreground/80">
        Loomplane can&apos;t find {zone.proofName} in public DNS any more, so
        new hostnames can&apos;t use it.{" "}
        {zone.mode === "managed"
          ? "Check again publishes it again."
          : "Add it back at the domain's DNS host, then check again."}
      </p>
      {through && (
        <p className="text-[13px] leading-normal text-foreground/80">
          This is {through}&apos;s record.
        </p>
      )}
      {/* Why Loomplane couldn't publish it, or why what it published isn't served. */}
      {zone.proofPublishError && (
        <p className="text-[13px] leading-normal text-foreground/80">
          {zone.proofPublishError}
        </p>
      )}
      <CheckAgain zone={zone} />
      <details className="text-[12.5px] text-muted-foreground">
        <summary className="cursor-pointer select-none">Record details</summary>
        <div className="pt-2">
          <ProofTable zone={zone} />
        </div>
      </details>
    </div>
  )
}

function Proof({ zone }: { zone: Zone }) {
  // A zone verified before TXT proofs were required has none to publish.
  if (!zone.proofName)
    return (
      <div className="flex flex-col gap-2.5 px-4 py-3">
        <p className="text-[13px] leading-normal text-foreground/80">
          Loomplane now checks a TXT record before projects can use a domain,
          and this one doesn&apos;t have one yet. Renew its proof to get the
          record to add.
        </p>
        <RenewProof zone={zone} />
      </div>
    )
  return zone.mode === "managed" ? (
    <ManagedProof zone={zone} />
  ) : (
    <TxtInstructions zone={zone} />
  )
}

function Hosting({ zone, label }: { zone: Zone; label: string }) {
  const connections = useQuery(useConnectionsList)
  const connection = connections.data?.connections.find(
    (c) => c?.id === zone.connectionId
  )
  const managed = zone.mode === "managed"
  const account = connection?.account || zone.accountId
  // A subdomain of a bigger provider zone has its records written there.
  const hostedIn =
    managed && zone.providerZone.name && zone.providerZone.name !== zone.name
      ? zone.providerZone.name
      : ""
  return (
    <div className="flex flex-col gap-1.5 px-4 py-3">
      <dl className="grid grid-cols-[minmax(0,120px)_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-[13px]">
        <Row label="DNS hosting">{label}</Row>
        {managed && zone.connectionId && (
          <Row label="Connection">{connection?.name ?? zone.connectionId}</Row>
        )}
        {managed && account && <Row label="Account">{account}</Row>}
        {hostedIn && (
          <Row label="Hosting zone">
            <span className="font-mono text-[12.5px]">{hostedIn}</span>
          </Row>
        )}
      </dl>
      {!managed && (
        <p className="text-[12.5px] text-muted-foreground">
          You add each hostname&apos;s record where the domain is hosted.
        </p>
      )}
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  )
}

function ZoneLink({ zone }: { zone: Zone }) {
  return (
    <Link
      to="/domains/$zoneId"
      params={{ zoneId: zone.id }}
      className="font-mono text-[12.5px] font-medium text-foreground underline-offset-4 hover:underline"
    >
      {zone.name}
    </Link>
  )
}
