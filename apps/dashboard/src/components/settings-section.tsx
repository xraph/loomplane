// A settings page's section: what it is and why on the left, its cards on
// the right. On narrow screens the two stack. The section is a region named
// by its heading, so each one is a landmark you can jump to.
import { type ReactNode, useId } from "react"

import { cn } from "@/lib/utils"

export function SettingsSection({
  title,
  description,
  aside,
  children,
  className,
}: {
  title: ReactNode
  description?: ReactNode
  /** Sits under the description: a status, a link to more. */
  aside?: ReactNode
  children: ReactNode
  className?: string
}) {
  const heading = useId()
  return (
    <section
      aria-labelledby={heading}
      className={cn(
        "grid gap-4 border-t border-border pt-6 lg:grid-cols-[minmax(0,17rem)_minmax(0,1fr)] lg:gap-10",
        className
      )}
    >
      <div className="grid content-start gap-1.5">
        <h2 id={heading} className="text-[15px] font-semibold tracking-tight">
          {title}
        </h2>
        {description && (
          <p className="text-[13px] leading-relaxed text-pretty text-muted-foreground">
            {description}
          </p>
        )}
        {aside}
      </div>
      <div className="grid min-w-0 content-start gap-4">{children}</div>
    </section>
  )
}

/** A card's own heading row inside a settings section. */
export function SettingsCardHead({
  title,
  description,
  action,
}: {
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="grid gap-1">
        <h3 className="text-[13.5px] font-semibold">{title}</h3>
        {description && (
          <p className="max-w-[60ch] text-[12.5px] leading-normal text-pretty text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {action}
    </div>
  )
}
