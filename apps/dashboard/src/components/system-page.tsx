import type { ReactNode } from "react"

import { OnboardingLayout } from "@/components/onboarding-layout"

/**
 * SystemPage is the full-page frame for states the app can't show inside
 * its shell: the API not answering, a session that won't load, a workspace
 * address that opens nothing. It's the onboarding layout with a row of
 * actions under its content.
 */
export function SystemPage({
  title,
  lede,
  tone = "neutral",
  children,
  actions,
}: {
  title: string
  lede: ReactNode
  /** problem marks the title as something that went wrong. */
  tone?: "neutral" | "problem"
  children?: ReactNode
  actions?: ReactNode
}) {
  return (
    <OnboardingLayout title={title} lede={lede} tone={tone}>
      {children}
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </OnboardingLayout>
  )
}

/** SystemAddress shows a web address, with one part of it marked. */
export function SystemAddress({
  before,
  marked,
  after,
}: {
  before: string
  marked?: string
  after?: string
}) {
  return (
    <span className="block rounded-xl bg-muted/50 px-3.5 py-3 font-mono text-[13px] break-all text-muted-foreground ring-1 ring-border ring-inset">
      {before}
      {marked && <span className="font-medium text-failed">{marked}</span>}
      {after}
    </span>
  )
}
