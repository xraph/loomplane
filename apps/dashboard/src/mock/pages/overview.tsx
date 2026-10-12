import {
  ActivityIcon,
  CheckIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CloudIcon,
  EllipsisIcon,
  GaugeIcon,
  KeyRoundIcon,
  PauseIcon,
  PlayIcon,
  PlusIcon,
  RocketIcon,
  RotateCcwIcon,
  ShieldCheckIcon,
  TimerIcon,
  WalletIcon,
  XIcon,
  ZapIcon,
} from "lucide-react"
import {
  type ComponentType,
  memo,
  type ReactNode,
  useMemo,
  useState,
} from "react"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  type ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"
import {
  Code,
  EnvBadge,
  ProviderGlyph,
  StatusDot,
  statusText,
} from "@/mock/components/bits"
import { BrandMark } from "@/components/brand-mark"
import {
  DotBarChart,
  DotProgress,
  DotRingChart,
  DottedLineChart,
  GlowBarChart,
} from "@/mock/components/dots"
import { LoomThread } from "@/mock/components/loom"
import { Page, PageHeading } from "@/components/page"
import { TopBar } from "@/mock/components/top-bar"
import { me, organization, person, providers } from "@/mock/data"
import { ProjectIcon } from "@/lib/project-style"
import { duration, money, relative } from "@/mock/format"
import {
  buildMinutes,
  commitToLive,
  latency,
  traffic,
  type TrafficRange,
  weeklyDeploys,
} from "@/mock/metrics"
import { navigate, to } from "@/mock/router"
import { useShell } from "@/mock/shell"
import { type DeploymentView, isLive, useStore } from "@/mock/store"

function greeting(now: number) {
  const h = new Date(now).getHours()
  if (h < 5) return "Working late"
  if (h < 12) return "Good morning"
  if (h < 18) return "Good afternoon"
  return "Good evening"
}

const compact = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
})

export function OverviewPage() {
  const { deployments, now } = useStore()
  return (
    <>
      <TopBar crumbs={[{ label: organization.name }, { label: "Overview" }]} />
      <Page className="gap-6">
        <PageHeading
          title={`${greeting(now)}, ${me.name.split(" ")[0]}`}
          description="Here's what's deploying, what it costs and how fast it ships."
        />
        <div className="grid gap-4 lg:grid-cols-12">
          <Hero deployments={deployments} now={now} />
          <DeploysCard />
          <div className="grid gap-4 sm:grid-cols-2 lg:col-span-5">
            <SpendCard />
            <LatencyCard />
          </div>
          <TrafficCard />
          <CommitCard />
          <QuickDeploy deployments={deployments} />
          <Activity deployments={deployments} now={now} />
          <Clouds />
        </div>
      </Page>
    </>
  )
}

/* ------------------------------------------------------------------------ */
/* Shared card parts                                                          */

type MenuItem = { label: string; onSelect: () => void }

function CardMenu({ title, items }: { title: string; items: MenuItem[] }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="-mt-1 -mr-1.5 text-muted-foreground"
            aria-label={`${title} options`}
          />
        }
      >
        <EllipsisIcon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        {items.map((m) => (
          <DropdownMenuItem key={m.label} onClick={m.onSelect}>
            {m.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** MetricCard is shadcn's Card with the overview's header: icon, title, menu. */
function MetricCard({
  icon: Icon,
  title,
  menu,
  className,
  children,
}: {
  icon: ComponentType<{ className?: string }>
  title: string
  menu?: MenuItem[]
  className?: string
  children: ReactNode
}) {
  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-[15px] font-normal text-muted-foreground">
          <Icon className="size-4" />
          {title}
        </CardTitle>
        {menu && (
          <CardAction>
            <CardMenu title={title} items={menu} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-1 flex-col gap-4">
        {children}
      </CardContent>
    </Card>
  )
}

function copyQuery(metric: string) {
  return () => {
    void navigator.clipboard?.writeText(
      `curl -H "Authorization: Bearer $LOOMPLANE_TOKEN" "https://loomplane.dev/api/v1/metrics/${metric}?org=${organization.slug}"`
    )
    toast.success("Copied the API request for this chart")
  }
}

function exportCsv(name: string) {
  return () => toast.success(`Exported ${name} as CSV`)
}

function Figure({
  value,
  delta,
  good = true,
  note,
}: {
  value: ReactNode
  delta?: string
  good?: boolean
  note?: ReactNode
}) {
  return (
    <div className="grid gap-1.5">
      <div className="flex items-baseline gap-2">
        <span className="text-[28px] leading-none font-semibold tracking-[-0.03em] tabular-nums">
          {value}
        </span>
        {delta && (
          <span
            className={cn(
              "text-sm font-medium tabular-nums",
              good ? "text-overdye" : "text-destructive"
            )}
          >
            {delta}
          </span>
        )}
      </div>
      {note && <span className="text-xs text-muted-foreground">{note}</span>}
    </div>
  )
}

/* ------------------------------------------------------------------------ */
/* Hero: what's in flight                                                     */

function Hero({
  deployments,
  now,
}: {
  deployments: DeploymentView[]
  now: number
}) {
  const { openDeploy } = useShell()
  const { redeploy } = useStore()
  const live = deployments.filter(isLive)
  const tickets = live.length
    ? live
    : deployments.filter((d) => d.status === "succeeded").slice(0, 1)
  const [index, setIndex] = useState(0)
  const [rollback, setRollback] = useState(false)
  const current = tickets[Math.min(index, tickets.length - 1)]
  const waiting = live.filter((d) => d.status === "waiting")
  const previous = current
    ? deployments.find(
        (d) =>
          d.project === current.project &&
          d.env === current.env &&
          d.status === "succeeded" &&
          d.id !== current.id
      )
    : undefined

  return (
    <Card className="hero-glow isolate lg:col-span-7 lg:row-span-2">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-[15px] font-normal text-muted-foreground">
          <RocketIcon className="size-4" />
          In flight
        </CardTitle>
        <CardDescription className="grid gap-2 pt-1">
          <span className="flex items-baseline gap-2 text-foreground">
            <span className="text-[44px] leading-none font-semibold tracking-[-0.03em] tabular-nums">
              {live.length}
            </span>
            <span className="text-2xl font-medium tracking-tight text-muted-foreground">
              {live.length === 1 ? "deployment" : "deployments"}
            </span>
          </span>
          {waiting.length
            ? `${waiting.length} waiting on you, ${live.length - waiting.length} running`
            : live.length
              ? "All running, nothing needs you"
              : "Nothing is deploying. Here's the last release."}
        </CardDescription>
      </CardHeader>

      <CardContent className="grid flex-1 content-center gap-4 sm:grid-cols-[minmax(0,1fr)_auto]">
        <div className="grid gap-2">
          <div className="relative h-[212px]">
            {tickets.length > 1 && (
              <Card className="absolute inset-x-5 -top-2.5 bottom-3 bg-card/50 backdrop-blur-sm" />
            )}
            {current && <Ticket d={current} now={now} />}
          </div>
          {tickets.length > 1 && (
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span className="tabular-nums">
                {Math.min(index, tickets.length - 1) + 1} of {tickets.length}
              </span>
              <div className="flex gap-1">
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="Previous deployment"
                  onClick={() =>
                    setIndex((i) => (i - 1 + tickets.length) % tickets.length)
                  }
                >
                  <ChevronLeftIcon />
                </Button>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="Next deployment"
                  onClick={() => setIndex((i) => (i + 1) % tickets.length)}
                >
                  <ChevronRightIcon />
                </Button>
              </div>
            </div>
          )}
        </div>
        <div className="grid grid-cols-3 gap-2 sm:flex sm:flex-col">
          <QuickAction
            icon={PlusIcon}
            label="Deploy"
            onClick={() => openDeploy()}
          />
          <QuickAction
            icon={ShieldCheckIcon}
            label="Approve"
            badge={waiting.length || undefined}
            disabled={!waiting.length}
            onClick={() =>
              waiting[0] && navigate(`/deployments/${waiting[0].id}`)
            }
          />
          <QuickAction
            icon={RotateCcwIcon}
            label="Roll back"
            disabled={!previous}
            onClick={() => setRollback(true)}
          />
        </div>
      </CardContent>

      <CardFooter className="flex-col items-stretch gap-3 border-t-0 bg-transparent">
        <div className="flex items-baseline justify-between text-[15px]">
          <span className="text-muted-foreground">Build minutes</span>
          <span className="text-muted-foreground tabular-nums">
            <span className="font-medium text-foreground">
              {Math.round((buildMinutes.used / buildMinutes.quota) * 100)}%
            </span>{" "}
            of {buildMinutes.quota.toLocaleString()} used
          </span>
        </div>
        <DotProgress
          value={buildMinutes.used / buildMinutes.quota}
          label={`${buildMinutes.used} of ${buildMinutes.quota} build minutes used`}
        />
      </CardFooter>

      <AlertDialog open={rollback} onOpenChange={setRollback}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Roll {current?.project} {current?.env} back?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Loomplane redeploys {previous?.sha}, the last release that went
              live, through the usual plan. Resources created since then stay.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep this release</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setRollback(false)
                if (!previous) return
                const id = redeploy(previous.id)
                navigate(`/deployments/${id}`)
                toast.success(
                  `Rolling ${previous.project} back to ${previous.sha}`
                )
              }}
            >
              Roll back
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function Ticket({ d, now }: { d: DeploymentView; now: number }) {
  const step = d.steps[d.current]
  const left =
    d.status === "running"
      ? d.steps
          .filter((s) => s.index > d.current)
          .reduce((a, s) => a + (d.durations[s.index] ?? 0) * 1000, 0) +
        (d.durations[d.current] ?? 0) * 1000 * (1 - (step?.progress ?? 0))
      : 0
  return (
    <a
      href={to(`/deployments/${d.id}`)}
      className="group absolute inset-0 rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <Card className="h-full justify-between bg-background/75 shadow-[0_18px_40px_-24px_rgb(0_0_0/0.35)] backdrop-blur-xl transition-transform group-hover:-translate-y-0.5">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-xs tracking-tight">
            <BrandMark className="size-4" />
            loomplane
          </CardTitle>
          <CardAction className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <StatusDot status={d.status} />
            {statusText[d.status]}
          </CardAction>
        </CardHeader>
        <CardContent className="grid gap-3">
          <div className="grid gap-1">
            <span className="flex items-center gap-2">
              <span className="text-xl font-semibold tracking-tight">
                {d.project}
              </span>
              <EnvBadge env={d.env} />
            </span>
            <span className="truncate text-sm text-muted-foreground">
              {d.message}
            </span>
          </div>
          <LoomThread steps={d.steps} />
          <div className="grid grid-cols-3 gap-3 text-xs">
            <span className="grid gap-0.5">
              <span className="text-muted-foreground">Started by</span>
              <span className="truncate font-medium">
                {person(d.author).name}
              </span>
            </span>
            <span className="grid gap-0.5">
              <span className="text-muted-foreground">Commit</span>
              <Code className="text-xs text-foreground">{d.sha}</Code>
            </span>
            <span className="grid gap-0.5 text-right">
              <span className="text-muted-foreground">
                {d.status === "running"
                  ? "Finishes in"
                  : d.status === "waiting"
                    ? "Waiting for"
                    : "Finished"}
              </span>
              <span className="font-medium tabular-nums">
                {d.status === "running"
                  ? duration(left)
                  : d.status === "waiting"
                    ? duration(now - (step?.startedAt ?? d.startedAt))
                    : relative(d.finishedAt ?? d.startedAt, now)}
              </span>
            </span>
          </div>
        </CardContent>
      </Card>
    </a>
  )
}

function QuickAction({
  icon: Icon,
  label,
  badge,
  disabled,
  onClick,
}: {
  icon: ComponentType<{ className?: string }>
  label: string
  badge?: number
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <Button
      variant="outline"
      disabled={disabled}
      onClick={onClick}
      className="relative h-14 justify-center gap-3 rounded-2xl bg-background px-4 shadow-[0_6px_16px_-10px_rgb(0_0_0/0.3)] sm:w-36 sm:justify-start"
    >
      <Icon className="size-5!" />
      <span className="hidden sm:inline">{label}</span>
      <span className="sr-only sm:hidden">{label}</span>
      {badge !== undefined && (
        <Badge className="absolute -top-2 -right-2 h-5 min-w-5 justify-center bg-weld px-1 text-black tabular-nums">
          {badge}
        </Badge>
      )}
    </Button>
  )
}

/* ------------------------------------------------------------------------ */
/* Metric cards                                                               */

function DeploysCard() {
  const total = weeklyDeploys.reduce((a, w) => a + w.value, 0)
  const last = weeklyDeploys.at(-1)?.value ?? 0
  const prev = weeklyDeploys.at(-2)?.value ?? 1
  const change = Math.round(((last - prev) / prev) * 100)
  return (
    <MetricCard
      icon={RocketIcon}
      title="Deployments"
      className="lg:col-span-5"
      menu={[
        {
          label: "View all deployments",
          onSelect: () => navigate("/deployments"),
        },
        { label: "Export CSV", onSelect: exportCsv("deployments") },
        { label: "Copy API request", onSelect: copyQuery("deployments") },
      ]}
    >
      <Figure
        value={total.toLocaleString()}
        delta={`${change >= 0 ? "+" : ""}${change}%`}
        good={change >= 0}
        note={`in 12 weeks, ${last} this week`}
      />
      <DotBarChart data={weeklyDeploys} />
    </MetricCard>
  )
}

function SpendCard() {
  const spend = providers.reduce((a, p) => a + p.spend, 0)
  const budget = providers.reduce((a, p) => a + p.budget, 0)
  return (
    <MetricCard
      icon={WalletIcon}
      title="Cloud spend"
      menu={[
        {
          label: "Open infrastructure",
          onSelect: () => navigate("/infrastructure"),
        },
        { label: "Export CSV", onSelect: exportCsv("spend") },
        { label: "Copy API request", onSelect: copyQuery("spend") },
      ]}
    >
      <Figure value={money(spend)} />
      <div className="flex items-center gap-4">
        <DotRingChart value={spend / budget} />
        <div className="grid gap-1.5 text-sm">
          <span className="flex items-center gap-2">
            <span className="size-1.5 rounded-full bg-weld" />
            Spent
          </span>
          <span className="flex items-center gap-2 text-muted-foreground">
            <span className="size-1.5 rounded-full bg-foreground/25" />
            Budget
          </span>
          <span className="text-xs text-muted-foreground tabular-nums">
            {Math.round((spend / budget) * 100)}% of {money(budget)}
          </span>
        </div>
      </div>
    </MetricCard>
  )
}

function LatencyCard() {
  const now = latency.thisWeek.at(-1) ?? 0
  const before = latency.lastWeek.at(-1) ?? 1
  const change = Math.round(((now - before) / before) * 100)
  return (
    <MetricCard
      icon={GaugeIcon}
      title="p95 latency"
      menu={[
        {
          label: "Break down by service",
          onSelect: () => navigate("/projects/atlas"),
        },
        { label: "Copy API request", onSelect: copyQuery("latency") },
      ]}
    >
      <Figure value={`${now}ms`} delta={`${change}%`} good={change <= 0} />
      <DottedLineChart
        labels={latency.labels}
        current={latency.thisWeek}
        previous={latency.lastWeek}
        unit="ms"
      />
    </MetricCard>
  )
}

const trafficConfig = {
  v: { label: "Requests", color: "var(--weld)" },
} satisfies ChartConfig

const ranges: { value: TrafficRange; label: string }[] = [
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
]

const TrafficCard = memo(function TrafficCard() {
  const [range, setRange] = useState<TrafficRange>("7d")
  const data = useMemo(() => traffic(range), [range])
  const fmt = (t: number) =>
    new Date(t).toLocaleString(
      undefined,
      range === "24h"
        ? { hour: "2-digit", minute: "2-digit" }
        : { month: "short", day: "numeric" }
    )
  return (
    <MetricCard
      icon={ActivityIcon}
      title="Traffic"
      className="lg:col-span-7 lg:row-span-2"
      menu={[
        { label: "Export CSV", onSelect: exportCsv("traffic") },
        { label: "Copy API request", onSelect: copyQuery("requests") },
      ]}
    >
      <ToggleGroup
        spacing={0}
        value={[range]}
        onValueChange={(v) => v[0] && setRange(v[0] as TrafficRange)}
        className="w-full rounded-xl bg-muted p-1"
        aria-label="Range"
      >
        {ranges.map((r) => (
          <ToggleGroupItem
            key={r.value}
            value={r.value}
            className="h-8 flex-1 rounded-lg text-muted-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-sm"
          >
            {r.label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
      <Figure
        value={compact.format(data.total)}
        delta="+18%"
        note="requests across every service"
      />
      <ChartContainer
        config={trafficConfig}
        className="aspect-auto min-h-64 w-full flex-1"
      >
        <AreaChart data={data.points} margin={{ left: 0, right: 8, top: 8 }}>
          <defs>
            <pattern
              id="traffic-dots"
              width="5"
              height="5"
              patternUnits="userSpaceOnUse"
            >
              <circle cx="2.5" cy="2.5" r="1.05" fill="var(--color-v)" />
            </pattern>
          </defs>
          <CartesianGrid vertical={false} strokeDasharray="3 3" />
          <XAxis
            dataKey="t"
            tickLine={false}
            axisLine={false}
            tickMargin={10}
            minTickGap={40}
            tickFormatter={(t: number) => fmt(t)}
          />
          <YAxis
            tickLine={false}
            axisLine={false}
            width={44}
            tickFormatter={(v: number) => compact.format(v)}
          />
          <ChartTooltip
            cursor={{ strokeDasharray: "3 3" }}
            content={
              <ChartTooltipContent
                indicator="line"
                labelFormatter={(_, p) => {
                  const t = p?.[0]?.payload?.t
                  return typeof t === "number" ? fmt(t) : ""
                }}
                formatter={(v) => `${compact.format(Number(v))} requests`}
              />
            }
          />
          <Area
            dataKey="v"
            type="linear"
            stroke="var(--color-v)"
            strokeWidth={1.75}
            fill="url(#traffic-dots)"
            fillOpacity={0.75}
            isAnimationActive={false}
          />
        </AreaChart>
      </ChartContainer>
    </MetricCard>
  )
})

function CommitCard() {
  const last = commitToLive.at(-1)?.value ?? 0
  const first = commitToLive[0]?.value ?? 1
  const change = Math.round(((last - first) / first) * 100)
  return (
    <MetricCard
      icon={TimerIcon}
      title="Commit to live"
      className="lg:col-span-5"
      menu={[{ label: "Copy API request", onSelect: copyQuery("lead-time") }]}
    >
      <Figure
        value={`${last}m`}
        delta={`${change}%`}
        good={change <= 0}
        note={`median, down from ${first}m a year ago`}
      />
      <GlowBarChart data={commitToLive} label="Commit to live" unit="m" />
    </MetricCard>
  )
}

function QuickDeploy({ deployments }: { deployments: DeploymentView[] }) {
  const { openDeploy, openNewProject } = useShell()
  const { projects } = useStore()
  return (
    <MetricCard
      icon={ZapIcon}
      title="Quick deploy"
      className="lg:col-span-5"
      menu={[{ label: "New project", onSelect: openNewProject }]}
    >
      <div className="grid grid-cols-3 gap-2 sm:grid-cols-5">
        {projects.map((p) => {
          const latest = deployments.find((d) => d.project === p.id)
          return (
            <Button
              key={p.id}
              variant="outline"
              onClick={() => openDeploy({ project: p.id })}
              className="h-auto flex-col gap-2 rounded-xl px-2 py-3"
            >
              <span className="relative flex">
                <ProjectIcon icon={p.icon} color={p.color} size="lg" />
                {latest && (
                  <span
                    aria-hidden="true"
                    className={cn(
                      "absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-card",
                      latest.status === "failed"
                        ? "bg-destructive"
                        : isLive(latest)
                          ? "bg-weld"
                          : "bg-overdye"
                    )}
                  />
                )}
              </span>
              <span className="truncate">{p.name}</span>
            </Button>
          )
        })}
        <Button
          variant="secondary"
          onClick={openNewProject}
          className="h-auto flex-col gap-2 rounded-xl px-2 py-3 text-muted-foreground"
        >
          <span className="flex size-10 items-center justify-center rounded-xl border border-dashed border-muted-foreground/40 bg-background">
            <PlusIcon className="size-4" />
          </span>
          New
        </Button>
      </div>
    </MetricCard>
  )
}

/* ------------------------------------------------------------------------ */
/* Activity and clouds                                                         */

type Event = {
  id: string
  ts: number
  icon: ReactNode
  tone: string
  text: ReactNode
  href?: string
}

const day = 86_400_000

function useEvents(deployments: DeploymentView[], now: number) {
  return useMemo(() => {
    const out: Event[] = []
    const where = (d: DeploymentView) => (
      <>
        <span className="font-medium text-foreground">{d.project}</span> to{" "}
        {d.env}
      </>
    )
    const first = (id: string) => person(id).name.split(" ")[0]
    for (const d of deployments.slice(0, 12)) {
      const href = `/deployments/${d.id}`
      const name = first(d.author)
      const end = d.finishedAt ?? d.startedAt
      if (d.status === "running") {
        out.push({
          id: `${d.id}-run`,
          ts: d.startedAt,
          icon: <PlayIcon />,
          tone: "bg-weld/15 text-weld-ink",
          text: (
            <>
              {name} is deploying {where(d)}
            </>
          ),
          href,
        })
      } else if (d.status === "waiting") {
        out.push({
          id: `${d.id}-wait`,
          ts: d.steps[d.current]?.startedAt ?? d.startedAt,
          icon: <PauseIcon />,
          tone: "bg-weld/15 text-weld-ink",
          text: <>The plan for {where(d)} needs approval</>,
          href,
        })
      } else if (d.status === "failed") {
        out.push({
          id: `${d.id}-fail`,
          ts: end,
          icon: <XIcon />,
          tone: "bg-destructive/12 text-destructive",
          text: (
            <>
              {name}&rsquo;s deploy of {where(d)} failed at{" "}
              {d.steps[d.current]?.step.name.toLowerCase()}
            </>
          ),
          href,
        })
      } else if (d.status === "canceled") {
        out.push({
          id: `${d.id}-cancel`,
          ts: end,
          icon: <RotateCcwIcon />,
          tone: "bg-muted text-muted-foreground",
          text: (
            <>
              {name} canceled a deploy of {where(d)}
            </>
          ),
          href,
        })
      } else {
        out.push({
          id: `${d.id}-ok`,
          ts: end,
          icon: <CheckIcon />,
          tone: "bg-overdye/12 text-overdye",
          text: (
            <>
              {name} deployed {where(d)}
            </>
          ),
          href,
        })
      }
      if (d.approvedAt && d.approvedBy && d.approvedAt <= now) {
        out.push({
          id: `${d.id}-approve`,
          ts: d.approvedAt,
          icon: <CheckIcon />,
          tone: "bg-muted text-foreground",
          text: (
            <>
              {first(d.approvedBy)} approved the plan for {where(d)}
            </>
          ),
          href,
        })
      }
    }
    out.push({
      id: "secret",
      ts: now - 2 * day,
      icon: <KeyRoundIcon />,
      tone: "bg-muted text-foreground",
      text: (
        <>
          Lin rotated{" "}
          <span className="font-mono text-foreground">STRIPE_SECRET_KEY</span>{" "}
          in production
        </>
      ),
    })
    out.push({
      id: "aws",
      ts: now - 5 * day,
      icon: <CloudIcon />,
      tone: "bg-muted text-foreground",
      text: (
        <>
          Jonas connected the AWS account{" "}
          <span className="font-mono text-foreground">acme-prod</span>
        </>
      ),
    })
    return out.sort((a, b) => b.ts - a.ts).slice(0, 7)
  }, [deployments, now])
}

function Activity({
  deployments,
  now,
}: {
  deployments: DeploymentView[]
  now: number
}) {
  const events = useEvents(deployments, now)
  return (
    <MetricCard
      icon={ActivityIcon}
      title="Activity"
      className="lg:col-span-7"
      menu={[
        {
          label: "Open the audit log",
          onSelect: () => toast("The full audit log isn't part of the mock"),
        },
      ]}
    >
      <ItemGroup className="gap-0.5">
        {events.map((e) => (
          <Item
            key={e.id}
            size="sm"
            className="-mx-3 py-2"
            render={e.href ? <a href={to(e.href)} /> : undefined}
          >
            <ItemMedia
              className={cn("size-7 rounded-full [&_svg]:size-3.5", e.tone)}
            >
              {e.icon}
            </ItemMedia>
            <ItemContent>
              <ItemDescription className="line-clamp-1 text-sm">
                {e.text}
              </ItemDescription>
            </ItemContent>
            <ItemActions className="text-xs text-muted-foreground tabular-nums">
              {relative(e.ts, now)}
            </ItemActions>
          </Item>
        ))}
      </ItemGroup>
    </MetricCard>
  )
}

function Clouds() {
  return (
    <MetricCard
      icon={CloudIcon}
      title="Clouds"
      className="lg:col-span-5"
      menu={[
        {
          label: "Open infrastructure",
          onSelect: () => navigate("/infrastructure"),
        },
      ]}
    >
      <ItemGroup className="gap-0.5">
        {providers.map((p) => (
          <Item
            key={p.id}
            size="sm"
            className="-mx-3"
            render={<a href={to("/infrastructure")} />}
          >
            <ItemMedia>
              <ProviderGlyph
                id={p.id}
                className="size-8 rounded-lg text-[10px]"
              />
            </ItemMedia>
            <ItemContent>
              <ItemTitle>
                {p.name}
                <span
                  className={cn(
                    "size-1.5 rounded-full",
                    p.status === "connected" ? "bg-overdye" : "bg-weld"
                  )}
                />
              </ItemTitle>
              <ItemDescription className="line-clamp-1 text-xs">
                {p.status === "degraded"
                  ? "Slow to answer, syncs running late"
                  : p.regions.join(", ")}
              </ItemDescription>
            </ItemContent>
            <ItemActions className="grid w-20 justify-items-end gap-1.5">
              <span className="text-sm tabular-nums">{money(p.spend)}</span>
              <DotProgress
                size="sm"
                className="w-20"
                value={p.spend / p.budget}
                label={`${p.name} spend against budget`}
              />
            </ItemActions>
          </Item>
        ))}
      </ItemGroup>
    </MetricCard>
  )
}
