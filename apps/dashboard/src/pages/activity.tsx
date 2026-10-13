// What the organization's Loomplane has done lately, from its own records.

import {
  ActivityIcon,
  CheckCircle2Icon,
  FileSearchIcon,
  GitPullRequestIcon,
  TriangleAlertIcon,
} from "lucide-react"
import { useState, type ReactNode } from "react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import { messageOf } from "@/client/errors"
import { useActivityGet } from "@/client/forge/loomplane/hooks"
import type { ActivitySummary } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Page, PageHeading, SectionHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  type ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

const windows = [7, 30, 90] as const

const chartConfig = {
  succeeded: { label: "Succeeded", color: "var(--chart-2)" },
  failed: { label: "Failed", color: "var(--destructive)" },
} satisfies ChartConfig

export function Activity() {
  const [days, setDays] = useState<number>(30)
  const activity = useQuery(useActivityGet, { query: { days } })

  return (
    <>
      <SiteHeader crumbs={[{ label: "Activity" }]} />
      <Page>
        <div className="flex flex-wrap items-end justify-between gap-4">
          <PageHeading
            title="Activity"
            description="What Loomplane has done for this organization lately, counted from its own records."
          />
          <ToggleGroup
            spacing={0}
            value={[String(days)]}
            onValueChange={(v) => v[0] && setDays(Number(v[0]))}
            className="rounded-xl bg-muted p-1"
            aria-label="How far back"
          >
            {windows.map((w) => (
              <ToggleGroupItem
                key={w}
                value={String(w)}
                className="h-8 rounded-lg px-3 text-muted-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-sm"
              >
                {w} days
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </div>

        {activity.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(activity.error, "Activity couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!activity.data && activity.status !== "error" && (
          <div className="grid gap-4 md:grid-cols-4">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-28 rounded-xl" />
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
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat
          icon={<CheckCircle2Icon />}
          title="Deployments"
          value={finished + d.running}
          note={
            finished === 0
              ? "None finished yet."
              : `${percent(d.succeeded, finished)} succeeded${d.retried ? `, ${d.retried} after a retry` : ""}.`
          }
        />
        <Stat
          icon={<ActivityIcon />}
          title="Typical deployment"
          value={d.medianSeconds ? duration(d.medianSeconds) : "None yet"}
          note={
            d.medianSeconds
              ? "Half the successful ones took less."
              : "Shown once a deployment succeeds."
          }
        />
        <Stat
          icon={<GitPullRequestIcon />}
          title="Plans decided"
          value={decided}
          note={
            data.plans.waiting
              ? `${data.plans.waiting} waiting for a decision now.`
              : "Nothing waiting for a decision."
          }
        />
        <Stat
          icon={<TriangleAlertIcon />}
          title="Drifted"
          value={data.drift.missing + data.drift.changed}
          note={`Of ${data.drift.deployed} deployed services and resources, from the last check.`}
        />
      </div>

      <SectionHeading
        title="Deployments by day"
        description="Each day's deployments that succeeded or failed, by when they started, in UTC."
      />
      <Card>
        <CardContent className="pt-6">
          {finished + d.running === 0 ? (
            <p className="py-16 text-center text-sm text-muted-foreground">
              No deployments in these {data.days} days.
            </p>
          ) : (
            <ChartContainer config={chartConfig} className="h-64 w-full">
              <BarChart
                data={data.daily}
                margin={{ left: 0, right: 8, top: 8 }}
              >
                <CartesianGrid vertical={false} strokeDasharray="3 3" />
                <XAxis
                  dataKey="date"
                  tickLine={false}
                  axisLine={false}
                  minTickGap={24}
                  tickFormatter={(v: string) => shortDate(v)}
                />
                <YAxis
                  allowDecimals={false}
                  tickLine={false}
                  axisLine={false}
                  width={28}
                />
                <ChartTooltip
                  content={
                    <ChartTooltipContent
                      labelFormatter={(v) => shortDate(String(v))}
                    />
                  }
                />
                <Bar
                  dataKey="succeeded"
                  stackId="d"
                  fill="var(--color-succeeded)"
                  radius={[0, 0, 2, 2]}
                />
                <Bar
                  dataKey="failed"
                  stackId="d"
                  fill="var(--color-failed)"
                  radius={[2, 2, 0, 0]}
                />
              </BarChart>
            </ChartContainer>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <GitPullRequestIcon className="size-4" /> Plans
            </CardTitle>
            <CardDescription>
              What became of the plans made in these {data.days} days.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Counts
              rows={[
                ["Approved", data.plans.approved],
                ["Rejected", data.plans.rejected],
                ["Expired before a decision", data.plans.expired],
                ["Replaced by a newer plan", data.plans.superseded],
                ["Couldn't be planned", data.plans.failed],
              ]}
            />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <FileSearchIcon className="size-4" /> Analyses
            </CardTitle>
            <CardDescription>
              Repository reads, by hand or after a push.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Counts
              rows={[
                ["Succeeded", data.analyses.succeeded],
                ["Failed", data.analyses.failed],
              ]}
            />
          </CardContent>
        </Card>
      </div>
      <p className="text-sm text-muted-foreground">
        Loomplane&apos;s own metrics, for Prometheus, are on{" "}
        <code className="font-mono">/_/metrics</code>. Backups are a command:{" "}
        <code className="font-mono">loomplane backup</code>, and{" "}
        <code className="font-mono">loomplane restore</code> to bring one back.
      </p>
    </>
  )
}

function Stat({
  icon,
  title,
  value,
  note,
}: {
  icon: ReactNode
  title: string
  value: ReactNode
  note: string
}) {
  return (
    <Card>
      <CardHeader>
        <CardDescription className="flex items-center gap-2 [&_svg]:size-4">
          {icon}
          {title}
        </CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      <CardContent className="text-sm text-muted-foreground">
        {note}
      </CardContent>
    </Card>
  )
}

function Counts({ rows }: { rows: [string, number][] }) {
  return (
    <dl className="grid gap-2 text-sm">
      {rows.map(([label, n]) => (
        <div key={label} className="flex justify-between gap-4">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="font-medium tabular-nums">{n}</dd>
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
