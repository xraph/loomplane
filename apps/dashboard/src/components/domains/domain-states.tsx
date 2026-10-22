// The state pills of the Domains mock. Light mode uses the mock's own
// colors; dark mode borrows the status tokens with the same meaning, and
// neither follows the accent.
import type { ReactNode } from "react"

import type { RouteStatus } from "@/client/forge/loomplane/types"
import {
  certificateLabel,
  dnsLabel,
  dnsTone,
  type DomainState,
  type PillTone,
  routeLabel,
  stateTone,
} from "@/lib/domains"
import { cn } from "@/lib/utils"

export const pillTones: Record<PillTone, string> = {
  ok: "bg-[#E5F0E9] text-[#2F6B48] dark:bg-overdye-tint dark:text-overdye-ink",
  warn: "bg-[#FBF1DD] text-[#7A5200] dark:bg-weld-tint dark:text-weld-ink",
  idle: "bg-[#F3F1EF] text-[#4A433E] dark:bg-muted dark:text-muted-foreground",
  failed: "bg-failed-tint text-destructive",
  own: "bg-[#E3EEF4] text-[#2F5C78] dark:bg-flight-tint dark:text-flight-ink",
}

export function DomainStatePill({
  tone,
  children,
  className,
}: {
  tone: PillTone
  children: ReactNode
  className?: string
}) {
  return (
    <span
      className={cn(
        "w-fit justify-self-start rounded-full px-2 py-px text-[11.5px] font-medium whitespace-nowrap",
        pillTones[tone],
        className
      )}
    >
      {children}
    </span>
  )
}

/** RouteStates is the DNS, Certificate and Route cells of a hostname row. */
export function RouteStates({ status }: { status: RouteStatus }) {
  const cells: [string, PillTone][] = [
    [dnsLabel(status), dnsTone(status)],
    [certificateLabel(status), stateTone(status.certificate as DomainState)],
    [routeLabel(status), stateTone(status.route as DomainState)],
  ]
  return (
    <>
      {cells.map(([label, tone], i) => (
        <div key={i} role="cell" className="min-w-0">
          <DomainStatePill tone={tone}>{label}</DomainStatePill>
        </div>
      ))}
    </>
  )
}
