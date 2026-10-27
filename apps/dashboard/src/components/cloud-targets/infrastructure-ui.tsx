import { cn } from "@/lib/utils"
import { Item, ItemGroup } from "@/components/ui/item"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Spinner } from "@/components/ui/spinner"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { useEffect, type ReactNode, type ComponentProps } from "react"
import { statusOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import type { ComponentType } from "react"
import { CloudsIcon } from "@/components/icons"
import { StatusPill, type Tone } from "@/components/runs"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
} from "@/components/zero-state"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"

export const cloudTabs = [
  "Targets",
  "Servers",
  "Domains",
  "Projects",
  "Activity",
] as const
export type CloudTab = (typeof cloudTabs)[number]
export function CloudTabs({
  value,
  onChange,
  children,
}: {
  value: CloudTab
  onChange: (value: CloudTab) => void
  children: ReactNode
}) {
  return (
    <Tabs
      value={value}
      onValueChange={(next) => {
        if (cloudTabs.includes(next as CloudTab)) onChange(next as CloudTab)
      }}
    >
      <TabsList variant="line" aria-label="Cloud views" className="flex-wrap">
        {cloudTabs.map((tab) => (
          <TabsTrigger key={tab} value={tab}>
            {tab}
          </TabsTrigger>
        ))}
      </TabsList>
      <TabsContent
        value={value}
        aria-label={value}
        className="grid min-w-0 gap-4"
      >
        {children}
      </TabsContent>
    </Tabs>
  )
}
/**
 * CloudEmpty is a Cloud page's empty state, in the shared design: a list
 * that will fill shows a faded preview of its rows, a prerequisite or a
 * missing page is a centered notice, and one inside a form or dialog stays
 * compact.
 */
export function CloudEmpty({
  title,
  children,
  action,
  kind = "list",
  icon: Icon = CloudsIcon,
}: {
  title: string
  children: ReactNode
  action?: ReactNode
  kind?: "list" | "notice" | "inline"
  icon?: ComponentType<{ className?: string }>
}) {
  return (
    <ZeroState
      variant={kind === "inline" ? "inline" : "panel"}
      preview={kind === "list"}
      className={kind === "inline" ? "gap-3 px-0 py-2" : undefined}
    >
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <Icon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{children}</ZeroStateDescription>
      </ZeroStateHeader>
      {action && <ZeroStateContent>{action}</ZeroStateContent>}
    </ZeroState>
  )
}
/**
 * CloudSectionHead is the head of a section on a Cloud page: its title with
 * an optional count, one line on what it holds, and its actions.
 */
export function CloudSectionHead({
  title,
  count,
  description,
  children,
}: {
  title: ReactNode
  count?: number
  description?: ReactNode
  /** The section's actions, on the right. */
  children?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="grid max-w-[64ch] min-w-0 gap-0.5">
        <h2 className="text-[15px] font-semibold">
          {title}
          {count !== undefined && " "}
          {count !== undefined && (
            <span className="font-mono text-xs font-normal text-muted-foreground tabular-nums">
              {count}
            </span>
          )}
        </h2>
        {description && (
          <p className="text-[12.5px] leading-relaxed text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {children && <div className="flex flex-wrap gap-2">{children}</div>}
    </div>
  )
}
export function CloudError({ message }: { message: string }) {
  return message ? (
    <Alert variant="destructive">
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  ) : null
}
export function Field({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <FormField className="grid min-w-0 gap-1 text-xs">
      <FieldLabel className="contents">
        {label}
        {children}
      </FieldLabel>
    </FormField>
  )
}
/**
 * listItemClass makes an Item a plain row of a divided list card. Item draws
 * its own transparent border, which hides the list's dividers, so a row
 * draws the divider itself.
 */
export const listItemClass =
  "rounded-none border-0 border-border px-4 not-last:border-b"
export function Rows({ children }: { children: ReactNode }) {
  return (
    <ItemGroup className="gap-0 overflow-hidden rounded-xl bg-card ring-1 ring-border">
      {children}
    </ItemGroup>
  )
}
export function Row({ className, ...props }: ComponentProps<typeof Item>) {
  return (
    <Item
      role="listitem"
      size="sm"
      className={cn("justify-between", listItemClass, className)}
      {...props}
    />
  )
}
export function CloudLoading({ children }: { children: ReactNode }) {
  return (
    <div
      role="status"
      className="flex items-center gap-2 text-[13px] text-muted-foreground"
    >
      <Spinner />
      {children}
    </div>
  )
}
// Servers, operations and syncs report free-form statuses. These name the
// tone StatusPill draws each in, and a status as a word rather than a key.
const liveStates = ["ready", "succeeded", "applied", "completed", "synced"]
const movingStates = ["running", "applying", "provisioning", "prepare"]
const waitingStates = ["pending", "waiting", "preview", "draining"]

export function stateTone(status: string): Tone {
  if (liveStates.includes(status)) return "live"
  if (status === "failed" || status === "error") return "failed"
  if (movingStates.includes(status)) return "flight"
  if (waitingStates.includes(status) || status.endsWith("_pending"))
    return "waiting"
  return "quiet"
}

/** StatePill is an infrastructure status in its tone. */
export function StatePill({ status }: { status: string }) {
  const word = status.replace(/_/g, " ")
  return (
    <StatusPill tone={stateTone(status)}>
      {word.charAt(0).toUpperCase() + word.slice(1)}
    </StatusPill>
  )
}

export const rowClass =
  "flex flex-wrap items-center justify-between gap-3 p-3 text-[13px]"

// Infrastructure operations do not publish every provider checkpoint on the
// project realtime stream. Keep their accepted state fresh independently.
export function useInfrastructureRefresh(
  active: boolean,
  refetch: () => Promise<unknown>
) {
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => {
      void refetch().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [active, refetch])
}

/** Setup selectors must not turn a failed read into an empty choice list. */
export function SetupReads({
  reads,
  children,
}: {
  reads: {
    label: string
    query: {
      status: string
      data?: unknown
      error?: unknown
      refetch: () => Promise<unknown>
    }
  }[]
  children: ReactNode
}) {
  const failed = reads.filter((read) => read.query.status === "error")
  if (failed.length)
    return (
      <div className="grid gap-2">
        {failed.map(({ label, query }) => (
          <div key={label}>
            {statusOf(query.error) === 403 && (
              <p className="mb-1 text-xs">{label} access is restricted.</p>
            )}
            <QueryError
              error={query.error}
              fallback={`${label} couldn't be loaded.`}
              retry={query.refetch}
            />
          </div>
        ))}
      </div>
    )
  if (reads.some((read) => !read.query.data))
    return (
      <CloudLoading>
        Loading {reads.map((read) => read.label.toLowerCase()).join(", ")}...
      </CloudLoading>
    )
  return children
}
