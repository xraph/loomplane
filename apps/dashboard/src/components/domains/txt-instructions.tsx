// The TXT record that proves a zone is the organization's, and a way to
// check for it again or, once it expires, to renew it. An external zone's
// owner adds the record; Loomplane publishes a managed zone's itself.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useDnsZonesUpdate,
  useDnsZonesVerify,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type { Zone } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import { cloudLabel } from "@/lib/clouds"
import { isSet, proofPublishState, relativeTime, useClock } from "@/lib/domains"

// RenewProof issues a new TXT value with a fresh 24 hours to publish it.
// Not in the mock: the mock's proofs never expire.
export function RenewProof({ zone }: { zone: Zone }) {
  const update = useMutation(useDnsZonesUpdate)
  const invalidate = useInvalidate()
  const [note, setNote] = useState("")
  const renew = async () => {
    setNote("")
    try {
      await update.mutateAsync({
        path: { zoneId: zone.id },
        body: {
          revision: zone.revision,
          projectIds: zone.projectIds,
          renewProof: true,
        },
      })
    } catch (err) {
      setNote(messageOf(err, "The proof wasn't renewed."))
    }
    invalidate(useDomainsOverview)
  }
  return (
    <div className="flex flex-wrap items-center gap-2.5">
      <Button
        variant="outline"
        size="sm"
        disabled={update.isPending}
        onClick={() => void renew()}
      >
        Renew proof
      </Button>
      <span className="text-[12.5px] text-muted-foreground" aria-live="polite">
        {note}
      </span>
    </div>
  )
}

export function TxtInstructions({ zone }: { zone: Zone }) {
  const now = useClock()
  return (
    <div className="flex flex-col gap-2.5 px-4 py-3">
      <p className="text-[13px] leading-normal text-foreground/80">
        Add this TXT record where{" "}
        <span className="font-mono text-[12.5px]">{zone.name}</span> is hosted,
        then check again. Projects can&apos;t use the domain until it&apos;s
        verified.
      </p>
      <ProofTable zone={zone} />
      {proofExpired(zone, now) ? (
        <>
          <p className="text-[12.5px] text-muted-foreground">
            This value expired {relativeTime(zone.proofExpiresAt, now)}, so it
            can&apos;t verify the domain any more. Renew it, then publish the
            new value.
          </p>
          <RenewProof zone={zone} />
        </>
      ) : (
        <CheckAgain zone={zone} />
      )}
    </div>
  )
}

/**
 * ManagedProof is where a managed zone's TXT record stands. Loomplane
 * writes it through the zone's own connection, so there is nothing to
 * copy; the record stays readable under its details.
 * Not in the mock, which asks for every record by hand.
 */
export function ManagedProof({ zone }: { zone: Zone }) {
  const now = useClock()
  const host = cloudLabel(zone.provider ?? "")
  const state = proofPublishState(zone)
  const line = {
    publishing: `Publishing the TXT record through ${host}.`,
    waiting: "Waiting for DNS to show the TXT record.",
    failed: zone.proofPublishError,
  }[state]
  return (
    <div className="flex flex-col gap-2.5 px-4 py-3">
      {proofExpired(zone, now) ? (
        <>
          <p className="text-[12.5px] text-muted-foreground">
            This value expired {relativeTime(zone.proofExpiresAt, now)}, so it
            can&apos;t verify the domain any more. Renew it and Loomplane
            publishes the new value.
          </p>
          <RenewProof zone={zone} />
        </>
      ) : (
        <>
          <p
            className="text-[13px] leading-normal text-foreground/80"
            aria-live="polite"
          >
            {line}
          </p>
          <CheckAgain zone={zone} />
        </>
      )}
      <details className="text-[12.5px] text-muted-foreground">
        <summary className="cursor-pointer select-none">Record details</summary>
        <div className="pt-2">
          <ProofTable zone={zone} />
        </div>
      </details>
    </div>
  )
}

// An expired proof can't verify; a renewed one starts the 24 hours again.
function proofExpired(zone: Zone, now: number): boolean {
  return (
    isSet(zone.proofExpiresAt) && new Date(zone.proofExpiresAt).getTime() <= now
  )
}

function ProofTable({ zone }: { zone: Zone }) {
  const cell = "font-mono break-all"
  return (
    <div
      role="table"
      aria-label="TXT record"
      className="grid grid-cols-[70px_minmax(0,1fr)_minmax(0,1fr)] gap-x-3 gap-y-1 rounded-[10px] bg-muted px-3 py-2.5 text-[12.5px]"
    >
      <div role="row" className="contents text-muted-foreground">
        <span role="columnheader">Type</span>
        <span role="columnheader">Name</span>
        <span role="columnheader">Value</span>
      </div>
      <div role="row" className="contents">
        <span role="cell" className={cell}>
          TXT
        </span>
        <span role="cell" className={cell}>
          {zone.proofName}
        </span>
        <span role="cell" className={cell}>
          {zone.proofValue}
        </span>
      </div>
    </div>
  )
}

function CheckAgain({ zone }: { zone: Zone }) {
  const verify = useMutation(useDnsZonesVerify)
  const invalidate = useInvalidate()
  const now = useClock()
  const [note, setNote] = useState("")
  const check = async () => {
    setNote("")
    try {
      await verify.mutateAsync({ path: { zoneId: zone.id } })
    } catch (err) {
      setNote(
        statusOf(err) === 409
          ? "Checked just now. The record isn't visible yet."
          : messageOf(err, "The record couldn't be checked.")
      )
    }
    // Every check saves on the zone, so later writes need its new revision.
    invalidate(useDomainsOverview)
  }
  return (
    <div className="flex flex-wrap items-center gap-2.5">
      <Button
        variant="outline"
        size="sm"
        disabled={verify.isPending}
        onClick={() => void check()}
      >
        Check again
      </Button>
      <span className="text-[12.5px] text-muted-foreground" aria-live="polite">
        {note ||
          (isSet(zone.proofCheckedAt)
            ? `Last checked ${relativeTime(zone.proofCheckedAt, now)}.`
            : "")}
      </span>
    </div>
  )
}
