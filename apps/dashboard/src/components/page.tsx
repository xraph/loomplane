import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

// SidebarInset is already the page's <main>, so this is a plain wrapper.
export function Page({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "mx-auto flex w-full max-w-[1240px] flex-1 flex-col gap-5 px-4 pt-6 pb-12 md:px-7",
        className
      )}
    >
      {children}
    </div>
  )
}

export function PageHeading({
  title,
  description,
  actions,
  eyebrow,
  children,
}: {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  eyebrow?: ReactNode
  children?: ReactNode
}) {
  return (
    <header className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div className="grid min-w-0 grid-cols-1 gap-1">
        {eyebrow}
        <h1 className="text-[22px] leading-tight font-semibold tracking-[-0.025em] text-balance md:text-2xl">
          {title}
        </h1>
        {description && (
          <p className="max-w-[72ch] text-[13.5px] leading-relaxed text-pretty text-muted-foreground">
            {description}
          </p>
        )}
        {children}
      </div>
      {actions && (
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {actions}
        </div>
      )}
    </header>
  )
}

export function SectionHeading({
  title,
  description,
  action,
  id,
}: {
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  id?: string
}) {
  return (
    <div id={id} className="flex scroll-mt-16 items-end justify-between gap-4">
      <div className="grid gap-1">
        <h2 className="text-base font-semibold tracking-tight">{title}</h2>
        {description && (
          <p className="text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {action}
    </div>
  )
}
