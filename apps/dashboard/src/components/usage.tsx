// Spend, latency and traffic, read from each connected cloud's own billing
// and monitoring. Loomplane never carries application traffic, so these
// are what the clouds report; a cloud that can't report says why, beside
// the connection to fix it on.
import { useState } from "react"

import { useUsageSpend, useUsageTraffic } from "@/client/forge/loomplane/hooks"
import type { UsageSource } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DotAreaChart } from "@/components/dots"
import { Figure, Panel, PanelHead } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cloudLabel } from "@/lib/clouds"
import { Link } from "@/lib/router"
import {
  change,
  compact,
  headline,
  latencyLabel,
  money,
  usageNotes,
  timeTicks,
} from "@/lib/usage"
import { cn } from "@/lib/utils"

const linkClass =
  "text-[12.5px] font-medium text-brand-ink hover:underline self-start"

function signed(pct: number) {
  return `${pct > 0 ? "+" : ""}${pct}%`
}

/** joined writes a list the way a sentence does: a, b and c. */
function joined(items: string[]): string {
  if (items.length < 2) return items.join("")
  return `${items.slice(0, -1).join(", ")} and ${items.at(-1)}`
}

/**
 * UsageNotes says which connections aren't reporting and what to do,
 * each reason once. Clouds with no API for it get one quiet line, since
 * there's nothing to do. A card shows `limit` reasons, and the rest
 * when asked.
 */
export function UsageNotes({
  sources,
  what,
  limit = Infinity,
}: {
  sources: UsageSource[]
  /** What the card shows, for the line about clouds with no API. */
  what: string
  limit?: number
}) {
  const [open, setOpen] = useState(false)
  const { fix, unsupported } = usageNotes(sources)
  if (fix.length === 0 && unsupported.length === 0) return null
  const shown = open ? fix : fix.slice(0, limit)
  const hidden = fix.length - shown.length
  return (
    <ul
      aria-label="Not reporting"
      className="mt-auto grid gap-1.5 border-t pt-3 text-[12.5px] leading-snug"
    >
      {shown.map((n) => (
        <li key={n.key} className="line-clamp-3 text-muted-foreground">
          {n.accounts.map((a, i) => (
            <span key={a.connectionId || i}>
              {i > 0 && (i === n.accounts.length - 1 ? " and " : ", ")}
              {a.connectionId ? (
                <Link
                  to="/clouds/accounts/$id"
                  params={{ id: a.connectionId }}
                  className="font-medium text-foreground hover:underline"
                >
                  {a.name}
                </Link>
              ) : (
                <span className="font-medium text-foreground">{a.name}</span>
              )}
            </span>
          ))}
          {n.services.length > 0 && <> ({joined(n.services)})</>}
          {": "}
          <span className={cn(n.failed && "text-weld")}>{n.reason}</span>
        </li>
      ))}
      {hidden > 0 && (
        <li className="text-muted-foreground">
          And{" "}
          <button
            type="button"
            onClick={() => setOpen(true)}
            className="font-medium text-foreground hover:underline"
          >
            {hidden} more
          </button>
        </li>
      )}
      {unsupported.length > 0 && (
        <li className="text-muted-foreground/80">
          {joined(unsupported)} {unsupported.length === 1 ? "doesn't" : "don't"}{" "}
          report {what} through an API Loomplane reads.
        </li>
      )}
    </ul>
  )
}

/** fixable is a source someone can do something about. */
const fixable = (s: UsageSource) =>
  s.status === "unavailable" || s.status === "failed"

/** quiet says why a row has no number, in two words. */
function quiet(sources: UsageSource[], empty: string): string {
  if (sources.length === 0) return empty
  return sources.some(fixable) ? "Needs a fix" : "Not reported"
}

/**
 * UsageSummary is the overview's one card for spend, latency and traffic:
 * a number for each when the clouds report it, two words when they
 * don't, and one link to the rest.
 */
export function UsageSummary({ className }: { className?: string }) {
  const spend = useQuery(useUsageSpend)
  const traffic = useQuery(useUsageTraffic, { query: { range: "7d" } })
  const s = spend.data
  const t = traffic.data
  const failed =
    spend.status === "error"
      ? spend
      : traffic.status === "error"
        ? traffic
        : undefined
  const connected = (s?.sources.length ?? 0) + (t?.sources.length ?? 0) > 0
  const lead = s ? headline(s.totals).lead : undefined
  const slowest = t?.services.find((x) => x.latency > 0)
  const empty = (row: string) => (connected ? row : "Not connected")
  const rows: [string, string | undefined, string][] = [
    [
      "Cloud spend",
      lead && money(lead.total, lead.currency),
      quiet(s?.sources ?? [], empty("Not reported")),
    ],
    [
      "p95 latency",
      slowest &&
        `${Math.round(slowest.latency)} ms${slowest.statistic === "average" ? " avg" : ""}`,
      quiet(t?.sources ?? [], empty("No services yet")),
    ],
    [
      "Traffic",
      t && t.services.length > 0
        ? `${compact.format(t.total)} this week`
        : undefined,
      quiet(t?.sources ?? [], empty("No services yet")),
    ],
  ]
  const live = rows.some(([, value]) => value)
  const missing = [...(s?.sources ?? []), ...(t?.sources ?? [])].some(fixable)
  return (
    <Panel
      dashed={!live}
      aria-label="Spend, latency and traffic"
      className={cn("gap-3", className)}
    >
      <PanelHead title="Spend, latency and traffic" />
      {failed ? (
        <QueryError
          error={failed.error}
          fallback="Spend and traffic couldn't be loaded."
          retry={failed.refetch}
        />
      ) : !s || !t ? (
        <Skeleton className="h-28 w-full" />
      ) : (
        <>
          <p className="text-[12.5px] leading-normal text-muted-foreground">
            {live
              ? "Read from each cloud's own billing and monitoring: this month's spend, and the last 7 days of traffic."
              : "These show up once Loomplane reads billing and metrics from your cloud accounts."}
          </p>
          <dl className="grid gap-1.5 text-[12.5px]">
            {rows.map(([label, value, why]) => (
              <div
                key={label}
                className="flex items-center justify-between gap-3"
              >
                <dt>{label}</dt>
                <dd
                  className={cn(
                    value ? "font-mono tabular-nums" : "text-muted-foreground"
                  )}
                >
                  {value ?? why}
                </dd>
              </div>
            ))}
          </dl>
          {connected ? (
            <Link to="/infrastructure" className={cn(linkClass, "mt-auto")}>
              {missing ? "See what's missing" : "See details"}
            </Link>
          ) : (
            <Link to="/integrations" className={cn(linkClass, "mt-auto")}>
              Connect a cloud
            </Link>
          )}
        </>
      )}
    </Panel>
  )
}

/** LatencyCard ranks services by latency over the last week. */
export function LatencyCard({ className }: { className?: string }) {
  const q = useQuery(useUsageTraffic, { query: { range: "7d" } })
  const r = q.data
  const slow = (r?.services ?? []).filter((s) => s.latency > 0).slice(0, 5)
  return (
    <Panel aria-label="Slowest services" className={cn("gap-4", className)}>
      <PanelHead title="Slowest services" aside="Last 7 days" />
      {q.status === "error" ? (
        <QueryError
          error={q.error}
          fallback="Latency couldn't be loaded."
          retry={q.refetch}
        />
      ) : !r ? (
        <Skeleton className="h-44 w-full" />
      ) : (
        <>
          {slow.length === 0 ? (
            <Figure
              value="None yet"
              note="Latency shows up once a service reports requests."
            />
          ) : (
            <ul className="grid gap-2.5">
              {slow.map((s) => {
                const pct = change(s.latency, s.previousLatency)
                return (
                  <li
                    key={s.bindingId}
                    className="flex items-baseline justify-between gap-3 text-[13px]"
                  >
                    <Link
                      to="/projects/$id"
                      params={{ id: s.projectId }}
                      className="min-w-0 truncate font-medium hover:underline"
                    >
                      {s.name}
                    </Link>
                    <span className="flex shrink-0 items-baseline gap-1.5 tabular-nums">
                      <span className="font-mono">
                        {Math.round(s.latency)} ms
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {latencyLabel(s.statistic)}
                      </span>
                      {pct !== null && (
                        <span
                          className={cn(
                            "text-xs",
                            pct <= 0 ? "text-overdye" : "text-destructive"
                          )}
                        >
                          {signed(pct)}
                        </span>
                      )}
                    </span>
                  </li>
                )
              })}
            </ul>
          )}
          <UsageNotes sources={r.sources} what="latency" limit={2} />
        </>
      )}
    </Panel>
  )
}

const ranges = [
  { value: "24h", label: "24 hours", short: "day" },
  { value: "7d", label: "7 days", short: "week" },
  { value: "30d", label: "30 days", short: "30 days" },
  { value: "90d", label: "90 days", short: "90 days" },
] as const

type Range = (typeof ranges)[number]["value"]

/** TrafficCard is every service's requests added up, over a chosen range. */
export function TrafficCard({ className }: { className?: string }) {
  const [range, setRange] = useState<Range>("7d")
  const q = useQuery(useUsageTraffic, { query: { range } })
  const r = q.data
  const span = ranges.find((x) => x.value === range)!
  const time = (t: number) =>
    new Date(t).toLocaleString(
      undefined,
      range === "24h"
        ? { hour: "2-digit", minute: "2-digit" }
        : { month: "short", day: "numeric" }
    )
  const pct = r ? change(r.total, r.previousTotal) : null
  const points = (r?.requests ?? []).map((p) => ({
    t: Date.parse(p.t),
    v: p.v,
  }))
  return (
    <Panel aria-label="Traffic" className={cn("gap-4", className)}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <PanelHead title="Traffic" />
        <ToggleGroup
          spacing={0}
          value={[range]}
          onValueChange={(v) => v[0] && setRange(v[0] as Range)}
          className="rounded-lg bg-muted p-0.5"
          aria-label="Range"
        >
          {ranges.map((x) => (
            <ToggleGroupItem
              key={x.value}
              value={x.value}
              aria-label={x.label}
              className="h-7 rounded-md px-2.5 text-xs text-muted-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-sm"
            >
              {x.value}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </div>
      {q.status === "error" ? (
        <QueryError
          error={q.error}
          fallback="Traffic couldn't be loaded."
          retry={q.refetch}
        />
      ) : !r ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <Figure
            value={compact.format(r.total)}
            note={
              r.services.length === 0
                ? "Requests show up once a service on a connected cloud reports them."
                : `requests across ${r.services.length} ${r.services.length === 1 ? "service" : "services"}${pct === null ? "" : ` · ${signed(pct)} on the ${span.short} before`}`
            }
          />
          {r.services.length > 0 && (
            <DotAreaChart
              data={points}
              label="Requests"
              ticks={timeTicks(
                points.map((p) => p.t),
                range
              )}
              formatTime={time}
              formatValue={(v) => compact.format(v)}
            />
          )}
          <UsageNotes sources={r.sources} what="requests" limit={2} />
        </>
      )}
    </Panel>
  )
}

/** SpendAccounts lists this month's spend by account, largest first. */
export function SpendAccounts() {
  const q = useQuery(useUsageSpend)
  const r = q.data
  if (q.status === "error")
    return (
      <QueryError
        error={q.error}
        fallback="Spend couldn't be loaded."
        retry={q.refetch}
      />
    )
  if (!r) return <Skeleton className="h-24 w-full" />
  if (r.sources.length === 0)
    return (
      <p className="text-[13px] text-muted-foreground">
        Connect a cloud account and what it costs shows up here.
      </p>
    )
  return (
    <div className="grid gap-4">
      {r.accounts.length > 0 && (
        <table className="w-full text-[13px]">
          <thead className="sr-only">
            <tr>
              <th>Account</th>
              <th>Cloud</th>
              <th>This month</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {r.accounts.map((a) => (
              <tr key={a.connectionId}>
                <td className="py-2">
                  <Link
                    to="/clouds/accounts/$id"
                    params={{ id: a.connectionId }}
                    className="font-medium hover:underline"
                  >
                    {a.name}
                  </Link>
                </td>
                <td className="py-2 text-muted-foreground">
                  {cloudLabel(a.cloud)}
                </td>
                <td className="py-2 text-right font-mono tabular-nums">
                  {a.currency ? money(a.total, a.currency) : "None yet"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <UsageNotes sources={r.sources} what="spend" />
    </div>
  )
}

/** AccountSpend is one account's spend this month, or why there's none. */
export function AccountSpend({ connectionId }: { connectionId: string }) {
  const q = useQuery(useUsageSpend)
  const r = q.data
  const account = r?.accounts.find((a) => a.connectionId === connectionId)
  const source = r?.sources.find((s) => s.connectionId === connectionId)
  let value = "None yet"
  let note = "Loomplane doesn't read spend for this kind of connection"
  if (q.status === "error") note = "Spend couldn't be loaded"
  else if (!r) note = "Reading the bill…"
  else if (account?.currency) {
    value = money(account.total, account.currency)
    note = "So far this month"
  } else if (account) note = "No charges so far this month"
  else if (source?.reason) note = source.reason
  return (
    <section className="flex items-center gap-4 rounded-xl bg-card p-4 ring-1 ring-border">
      <div
        aria-hidden="true"
        className="canvas-dots size-16 shrink-0 rounded-full opacity-60"
      />
      <div className="grid gap-1">
        <span className="text-[12.5px] text-muted-foreground">
          Spend this month
        </span>
        <span
          className={cn(
            "text-lg font-semibold",
            !account?.currency && "text-muted-foreground"
          )}
        >
          {value}
        </span>
        <span className="text-[12.5px] leading-snug text-muted-foreground">
          {note}
        </span>
      </div>
    </section>
  )
}
