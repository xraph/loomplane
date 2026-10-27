// A registered domain that renews soon, or lapses with auto-renew off, and
// the switch for it. The renewal notice links here; arriving from it only
// moves focus to the switch.
import { CalendarIcon } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useDomainRegistrationsList,
  useDomainRegistrationsSetAutoRenew,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type { Registration } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import { formatDay, formatPrice } from "@/lib/domains"
import { cn } from "@/lib/utils"

export function RenewalBanner({
  registration,
  focus = false,
}: {
  registration: Registration
  /** Scroll to and focus the switch, for the renewal notice's link. */
  focus?: boolean
}) {
  const setAutoRenew = useMutation(useDomainRegistrationsSetAutoRenew)
  const invalidate = useInvalidate()
  const button = useRef<HTMLButtonElement>(null)
  const [saved, setSaved] = useState<Registration>()
  const [error, setError] = useState("")
  // What the switch wrote, until the lists catch up with it.
  const r =
    saved && saved.revision >= registration.revision ? saved : registration

  useEffect(() => {
    if (!focus) return
    button.current?.scrollIntoView?.({ block: "center" })
    button.current?.focus()
  }, [focus])

  const flip = async () => {
    setError("")
    try {
      setSaved(
        await setAutoRenew.mutateAsync({
          path: { registrationId: r.id },
          body: { autoRenew: !r.autoRenew, revision: r.revision },
        })
      )
    } catch (err) {
      // Not in the mock: what a failed switch says.
      setError(
        statusOf(err) === 409
          ? "This registration changed. Check it and try again."
          : messageOf(err, "Auto-renew wasn't changed.")
      )
    }
    invalidate(useDomainsOverview)
    invalidate(useDomainRegistrationsList)
  }

  const day = formatDay(r.domainExpiresAt)
  const price = formatPrice(r.quote.currency, r.quote.renewalCost)
  return (
    <div
      role="status"
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-2.5 rounded-xl px-3.5 py-[11px]",
        r.autoRenew
          ? "bg-[#E3EEF4] text-[#1D3B4F] dark:bg-flight-tint dark:text-flight-ink"
          : "bg-[#FBF1DD] text-[#5C3E00] dark:bg-weld-tint dark:text-weld-ink"
      )}
    >
      <CalendarIcon className="size-4 flex-none" strokeWidth={1.9} />
      <span className="flex-[1_1_360px] text-[13px] leading-normal">
        {r.autoRenew
          ? `${r.name} renews on ${day}${price ? ` for ${price}` : ""}, charged to your Cloudflare account. Auto-renew is on.`
          : `Auto-renew is off for ${r.name}. It expires on ${day} unless you renew it.`}
        {error && <span className="block text-destructive">{error}</span>}
      </span>
      <Button
        ref={button}
        variant="outline"
        size="sm"
        className="text-foreground"
        disabled={setAutoRenew.isPending}
        onClick={() => void flip()}
      >
        {r.autoRenew ? "Turn off auto-renew" : "Turn auto-renew back on"}
      </Button>
    </div>
  )
}
