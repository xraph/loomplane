// What the organization's Loomplane has done lately, from its own records.

import { useState, type ReactNode } from "react"

import { useActivityGet } from "@/client/forge/loomplane/hooks"
import type { ActivitySummary } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DotStackChart } from "@/components/dots"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"

const windows = [7, 30, 90] as const

export function Activity() {
  const [days, setDays] = useState<number>(30)
  const activity = useQuery(useActivityGet, { query: { days } })

  return (
    <>
      <SiteHeader crumbs={[{ label: "Activity" }]} />
      <Page>
        <PageHeading
          title="Activity"
          description="What Loomplane has done for this workspace lately, counted from its own records."
          actions={
            <ToggleGroup
              spacing={0.5}
              value={[String(days)]}
              onValueChange={(v) => v[0] && setDays(Number(v[0]))}
              className="rounded-lg bg-muted p-0.5"
              aria-label="How far back"
            >
              {windows.map((w) => (
                <ToggleGroupItem
                  key={w}
                  value={String(w)}
                  className="h-[30px] rounded-lg px-3 text-[13px] font-medium text-muted-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-sm"
                >
                  {w} days
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          }
        />

        {activity.status === "error" && (
          <QueryError
            error={activity.error}
            fallback="Activity couldn't be loaded."
            retry={activity.refetch}
          />
        )}
        {!activity.data && activity.status !== "error" && (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-[118px] rounded-[14px]" />
            ))}
          </div>
        )}
        {activity.data && <Summary data={activity.data} />}
      </Page>
    </>
  )
}

function Summary({ data }: { data: ActivitySummary }) {
  const d = data.deployments
  const finished = d.succeeded + d.failed
  const decided = data.plans.approved + data.plans.rejected + data.plans.expired
  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Stat
          title="Deployments"
          value={finished + d.running}
          note={
            finished === 0
              ? "None finished yet."
              : `${percent(d.succeeded, finished)} succeeded${d.retried ? `, ${d.retried} after a retry` : ""}.`
          }
        />
        <Stat
          title="Typical deployment"
          value={d.medianSeconds ? duration(d.medianSeconds) : "None yet"}
          note={
            d.medianSeconds
              ? "Half the successful ones took less."
              : "Shown once a deployment succeeds."
          }
        />
        <Stat
          title="Plans decided"
          value={decided}
          note={
            data.plans.waiting
              ? `${data.plans.waiting} waiting for a decision now.`
              : "Nothing waiting for a decision."
          }
        />
        <Stat
          title="Drifted"
          value={data.drift.missing + data.drift.changed}
          note={`Of ${data.drift.deployed} deployed services and resources, from the last check.`}
        />
      </div>

      <Panel aria-label="Deployments by day" className="gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <CardHeading
            title="Deployments by day"
            description="Each day's deployments that succeeded or failed, by when they started, in UTC."
          />
          <div className="flex gap-3.5 text-[12.5px] text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-[3px] bg-overdye" />
              Succeeded
            </span>
            <span className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-[3px] bg-failed" />
              Failed
            </span>
          </div>
        </div>
        {finished + d.running === 0 ? (
          <p className="py-14 text-center text-[13px] text-muted-foreground">
            No deployments in these {data.days} days.
          </p>
        ) : (
          <DotStackChart
            data={data.daily.map((d) => ({
              label: shortDate(d.date),
              succeeded: d.succeeded,
              failed: d.failed,
            }))}
          />
        )}
      </Panel>

      <div className="grid items-start gap-3 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <Panel aria-label="Plans" className="gap-0 p-0">
          <CardHeading
            className="border-b px-4 py-3"
            title="Plans"
            description={`What became of the plans made in these ${data.days} days.`}
          />
          <Bars
            rows={[
              ["Approved", data.plans.approved, "bg-overdye"],
              ["Waiting for a decision", data.plans.waiting, "bg-weld"],
              ["Rejected", data.plans.rejected, "bg-failed"],
              ["Expired before a decision", data.plans.expired, "bg-warp"],
              ["Replaced by a newer plan", data.plans.superseded, "bg-warp"],
              ["Couldn't be planned", data.plans.failed, "bg-failed"],
            ]}
          />
        </Panel>
        <Panel aria-label="Analyses" className="gap-0 p-0">
          <CardHeading
            className="border-b px-4 py-3"
            title="Analyses"
            description="Repository reads, by hand or after a push."
          />
          <dl className="divide-y text-[13px]">
            {(
              [
                ["Succeeded", data.analyses.succeeded],
                ["Failed", data.analyses.failed],
              ] as const
            ).map(([label, n]) => (
              <div
                key={label}
                className="flex items-center justify-between gap-4 px-4 py-2.5"
              >
                <dt>{label}</dt>
                <dd className="font-mono tabular-nums">{n}</dd>
              </div>
            ))}
          </dl>
        </Panel>
      </div>
      <p className="text-[12.5px] text-muted-foreground">
        Loomplane&apos;s own metrics, for Prometheus, are on{" "}
        <code className="font-mono">/_/metrics</code>. Backups are a command:{" "}
        <code className="font-mono">loomplane backup</code>, and{" "}
        <code className="font-mono">loomplane restore</code> to bring one back.
      </p>
    </>
  )
}

/** CardHeading is a card's 14px title with its line of explanation. */
function CardHeading({
  title,
  description,
  className,
}: {
  title: string
  description: string
  className?: string
}) {
  return (
    <div className={cn("grid gap-0.5", className)}>
      <h2 className="text-[14px] font-semibold">{title}</h2>
      <p className="text-[12.5px] text-muted-foreground">{description}</p>
    </div>
  )
}

function Stat({
  title,
  value,
  note,
}: {
  title: string
  value: ReactNode
  note: string
}) {
  return (
    <Panel className="gap-1.5">
      <h2 className="text-[12.5px] font-medium text-muted-foreground">
        {title}
      </h2>
      <span className="font-mono text-[26px] leading-none font-medium tracking-[-0.02em] tabular-nums">
        {value}
      </span>
      <span className="text-[12.5px] leading-[1.45] text-muted-foreground">
        {note}
      </span>
    </Panel>
  )
}

/** Bars counts each outcome with a bar against the largest. */
function Bars({ rows }: { rows: [string, number, string][] }) {
  const max = Math.max(1, ...rows.map(([, n]) => n))
  return (
    <dl className="divide-y">
      {rows.map(([label, n, tone]) => (
        <div key={label} className="flex items-center gap-3.5 px-4 py-2.5">
          <dt className="w-[210px] max-w-[45%] shrink-0 text-[13px]">
            {label}
          </dt>
          <dd
            aria-hidden="true"
            className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted"
          >
            <span
              className={cn("block h-full rounded-full", tone)}
              style={{ width: `${Math.round((n / max) * 100)}%` }}
            />
          </dd>
          <dd className="w-8 shrink-0 text-right font-mono text-[12.5px] tabular-nums">
            {n}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function percent(part: number, whole: number): string {
  return `${Math.round((part / whole) * 100)}%`
}

/** duration says a number of seconds the way a person would. */
export function duration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  if (m < 60) return s ? `${m}m ${s}s` : `${m}m`
  const h = Math.floor(m / 60)
  return m % 60 ? `${h}h ${m % 60}m` : `${h}h`
}

function shortDate(iso: string): string {
  const [y, mo, d] = iso.split("-").map(Number)
  return new Date(Date.UTC(y, mo - 1, d)).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  })
}
