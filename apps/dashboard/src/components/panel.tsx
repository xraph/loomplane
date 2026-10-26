// The overview's cards: a quiet panel, a heading with something aside, and
// a big figure with a note. Dashed panels hold the place of what isn't
// there yet.
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export function Panel({
  className,
  dashed = false,
  children,
  ...props
}: React.ComponentProps<"section"> & { dashed?: boolean }) {
  return (
    <section
      className={cn(
        "flex min-w-0 flex-col rounded-[14px] px-4 py-3.5",
        dashed
          ? "border border-dashed border-border bg-subtle"
          : "bg-card ring-1 ring-border",
        className
      )}
      {...props}
    >
      {children}
    </section>
  )
}

export function PanelHead({
  title,
  aside,
  className,
}: {
  title: ReactNode
  aside?: ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex items-baseline justify-between gap-3", className)}>
      <h2 className="text-[13px] font-semibold">{title}</h2>
      {aside && (
        <span className="shrink-0 text-xs text-muted-foreground">{aside}</span>
      )}
    </div>
  )
}

export function Figure({
  value,
  note,
}: {
  value: ReactNode
  note?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <span className="font-mono text-[26px] leading-none font-medium tracking-[-0.02em] tabular-nums">
        {value}
      </span>
      {note && (
        <span className="text-[12.5px] text-muted-foreground">{note}</span>
      )}
    </div>
  )
}
