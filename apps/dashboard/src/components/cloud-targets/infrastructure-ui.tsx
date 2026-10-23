import { useEffect, type ReactNode } from "react"
import { statusOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import { CloudIcon } from "lucide-react"
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
export function CloudEmpty({
  title,
  children,
  action,
}: {
  title: string
  children: ReactNode
  action?: ReactNode
}) {
  return (
    <ZeroState variant="inline" preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <CloudIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{children}</ZeroStateDescription>
      </ZeroStateHeader>
      {action && <ZeroStateContent>{action}</ZeroStateContent>}
    </ZeroState>
  )
}
export function CloudError({ message }: { message: string }) {
  return message ? (
    <p role="alert" className="text-sm text-destructive">
      {message}
    </p>
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
    <label className="grid min-w-0 gap-1 text-xs">
      {label}
      {children}
    </label>
  )
}
export function Rows({ children }: { children: ReactNode }) {
  return (
    <div className="divide-y rounded-xl bg-card ring-1 ring-border">
      {children}
    </div>
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
      <p role="status">
        Loading {reads.map((read) => read.label.toLowerCase()).join(", ")}...
      </p>
    )
  return children
}
