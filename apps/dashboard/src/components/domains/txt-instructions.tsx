// The TXT record that proves an external zone is the organization's, and
// a way to check for it again.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useDnsZonesVerify,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type { Zone } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import { isSet, relativeTime, useClock } from "@/lib/domains"

export function TxtInstructions({ zone }: { zone: Zone }) {
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

  const cell = "font-mono break-all"
  return (
    <div className="flex flex-col gap-2.5 px-4 py-3">
      <p className="text-[13px] leading-normal text-foreground/80">
        Add this TXT record where{" "}
        <span className="font-mono text-[12.5px]">{zone.name}</span> is hosted,
        then check again. Projects can&apos;t use the domain until it&apos;s
        verified.
      </p>
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
      <div className="flex flex-wrap items-center gap-2.5">
        <Button
          variant="outline"
          size="sm"
          disabled={verify.isPending}
          onClick={() => void check()}
        >
          Check again
        </Button>
        <span
          className="text-[12.5px] text-muted-foreground"
          aria-live="polite"
        >
          {note ||
            (isSet(zone.proofCheckedAt)
              ? `Last checked ${relativeTime(zone.proofCheckedAt, now)}.`
              : "")}
        </span>
      </div>
    </div>
  )
}
