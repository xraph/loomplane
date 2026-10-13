// The overview: what's deploying right now, how often and how fast the
// organization ships, and where it runs. Cards whose numbers Loomplane
// doesn't collect yet (spend, latency, traffic, build minutes) keep their
// place with an empty state that says what they're waiting for, so the
// layout is the one the data will fill.
import { useAuth } from "@authsome/ui-react"
import {
  ActivityIcon,
  CheckIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CloudIcon,
  EllipsisIcon,
  GaugeIcon,
  type LucideIcon,
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
import { type ReactNode, useMemo, useState } from "react"

import {
  useActivityGet,
  useConnectionsList,
} from "@/client/forge/loomplane/hooks"
import type { Binding } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { BrandMark } from "@/components/brand-mark"
import { CloudGlyph } from "@/components/cloud-glyph"
import { DotBarChart, DotProgress, GlowBarChart } from "@/components/dots"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { Code, EnvBadge, RunDot, Thread } from "@/components/runs"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
} from "@/components/ui/empty"
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
import { Skeleton } from "@/components/ui/skeleton"
import { cloudLabel } from "@/lib/clouds"
import { duration, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import {
  commitLabel,
  elapsed,
  isLive,
  type Run,
  runStatusText,
} from "@/lib/runs"
import { useNow, usePeople, useRunning, useRuns } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

// Twelve weeks of deploys, the span the mock's chart covers.
const weeks = 12
const daysShown = weeks * 7

function greeting(now: number) {
  const h = new Date(now).getHours()
  if (h < 5) return "Working late"
  if (h < 12) return "Good morning"
  if (h < 18) return "Good afternoon"
  return "Good evening"
}

export function Overview() {
  const { user } = useAuth()
  const now = useNow()
  const { runs, projects, loaded, error, refetch } = useRuns()
  const first = user?.first_name?.trim()
  return (
    <>
      <SiteHeader crumbs={[{ label: "Overview" }]} />
      <Page className="gap-6">
        <PageHeading
          title={first ? `${greeting(now)}, ${first}` : greeting(now)}
          description="Here's what's deploying, how often you ship and where it runs."
        />
        {error !== undefined && (
          <QueryError
            error={error}
            fallback="Some deployments couldn't be loaded."
            retry={refetch}
          />
        )}
        <div className="grid gap-4 lg:grid-cols-12">
          {(error === undefined || runs.length > 0) && (
            <Hero runs={runs} loaded={loaded} now={now} />
          )}
          <DeploysCard />
          <div className="grid gap-4 sm:grid-cols-2 lg:col-span-5">
            <PendingCard
              icon={WalletIcon}
              title="Cloud spend"
              waiting="Spend arrives with billing from your cloud connections."
              shape="ring"
            />
            <PendingCard
              icon={GaugeIcon}
              title="p95 latency"
              waiting="Latency arrives once Loomplane reads your services' metrics."
              shape="line"
            />
          </div>
          <PendingCard
            icon={ActivityIcon}
            title="Traffic"
            className="lg:col-span-7 lg:row-span-2"
            waiting="Requests across every service arrive with the same metrics, by the hour and by the day."
            shape="area"
          />
          <CommitCard />
          <QuickDeploy
            runs={runs}
            projectsLoaded={loaded}
            projects={projects}
          />
          {(error === undefined || runs.length > 0) && (
            <Feed runs={runs} now={now} />
          )}
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
  icon: LucideIcon
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

/**
 * PendingCard holds the place of a metric Loomplane doesn't collect yet:
 * the card, a quiet field of dots in the chart's shape, and what it waits
 * for.
 */
function PendingCard({
  icon,
  title,
  waiting,
  shape,
  className,
}: {
  icon: LucideIcon
  title: string
  waiting: string
  shape: "ring" | "line" | "area"
  className?: string
}) {
  return (
    <MetricCard icon={icon} title={title} className={className}>
      <Figure value={<span className="text-muted-foreground">None yet</span>} />
      <div
        aria-hidden="true"
        className={cn(
          "canvas-dots rounded-lg opacity-60",
          shape === "ring" && "size-24 rounded-full",
          shape === "line" && "h-24 w-full",
          shape === "area" && "min-h-64 w-full flex-1"
        )}
      />
      <p className="text-xs text-muted-foreground">{waiting}</p>
    </MetricCard>
  )
}

/* ------------------------------------------------------------------------ */
/* Hero: what's in flight                                                     */

function Hero({
  runs,
  loaded,
  now,
}: {
  runs: Run[]
  loaded: boolean
  now: number
}) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const live = runs.filter(isLive)
  const tickets = live.length
    ? live
    : runs.filter((r) => r.status === "succeeded").slice(0, 1)
  const [index, setIndex] = useState(0)
  const current = tickets[Math.min(index, tickets.length - 1)]
  const waiting = live.filter((r) => r.status === "waiting")
  const failed = runs.find((r) => r.status === "failed")

  return (
    <Card
      className={cn(
        "hero-glow isolate lg:col-span-7",
        (current || !loaded) && "lg:row-span-2"
      )}
    >
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
              : current
                ? "Nothing is deploying. Here's the last release."
                : "Nothing has been deployed yet."}
        </CardDescription>
      </CardHeader>

      <CardContent
        className={cn(
          "grid flex-1 content-center gap-4",
          current && "sm:grid-cols-[minmax(0,1fr)_auto]"
        )}
      >
        <div className="grid gap-2">
          <div className="relative h-[212px]">
            {tickets.length > 1 && (
              <Card className="absolute inset-x-5 -top-2.5 bottom-3 bg-card/50 backdrop-blur-sm" />
            )}
            {current ? (
              <Ticket run={current} now={now} />
            ) : !loaded ? (
              <Skeleton className="absolute inset-0 rounded-xl" />
            ) : (
              <Empty className="absolute inset-0 bg-background/60 backdrop-blur-xl">
                <EmptyHeader>
                  <EmptyTitle>Plan your first deployment</EmptyTitle>
                  <EmptyDescription>
                    Choose a project and environment to review what deploying
                    would change.
                  </EmptyDescription>
                </EmptyHeader>
                {can("deployment:create") && (
                  <EmptyContent>
                    <Button size="sm" onClick={() => openDeploy()}>
                      New deployment
                    </Button>
                  </EmptyContent>
                )}
              </Empty>
            )}
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
        {current && (
          <div className="grid grid-cols-3 gap-2 sm:flex sm:flex-col">
            <QuickAction
              icon={PlusIcon}
              label="Deploy"
              disabled={!can("deployment:create")}
              onClick={() => openDeploy()}
            />
            <QuickAction
              icon={ShieldCheckIcon}
              label="Approve"
              badge={waiting.length || undefined}
              disabled={!waiting.length}
              onClick={() =>
                waiting[0] &&
                void navigate({
                  to: "/deployments/$id",
                  params: { id: waiting[0].id },
                })
              }
            />
            <QuickAction
              icon={RotateCcwIcon}
              label="Retry"
              disabled={!failed}
              onClick={() =>
                failed &&
                void navigate({
                  to: "/deployments/$id",
                  params: { id: failed.id },
                })
              }
            />
          </div>
        )}
      </CardContent>

      <CardFooter className="flex-col items-stretch gap-3 border-t-0 bg-transparent">
        <div className="flex items-baseline justify-between text-[15px]">
          <span className="text-muted-foreground">Build minutes</span>
          <span className="text-sm text-muted-foreground">Not metered yet</span>
        </div>
        <DotProgress value={0} label="Build minutes aren't metered yet" />
      </CardFooter>
    </Card>
  )
}

function Ticket({ run: r, now }: { run: Run; now: number }) {
  const person = usePeople()
  const started = Date.parse(r.createdAt)
  return (
    <Link
      to="/deployments/$id"
      params={{ id: r.id }}
      className="group absolute inset-0 rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <Card className="h-full justify-between bg-background/75 shadow-[0_18px_40px_-24px_rgb(0_0_0/0.35)] backdrop-blur-xl transition-transform group-hover:-translate-y-0.5">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-xs tracking-tight">
            <BrandMark className="size-4" />
            loomplane
          </CardTitle>
          <CardAction className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <RunDot status={r.status} />
            {runStatusText[r.status]}
          </CardAction>
        </CardHeader>
        <CardContent className="grid gap-3">
          <div className="grid gap-1">
            <span className="flex items-center gap-2">
              <span className="truncate text-xl font-semibold tracking-tight">
                {r.project?.name ?? "A deleted project"}
              </span>
              <EnvBadge env={r.environment} />
            </span>
            <span className="truncate text-sm text-muted-foreground">
              {r.phase}
            </span>
          </div>
          <Thread steps={r.thread} />
          <div className="grid grid-cols-3 gap-3 text-xs">
            <span className="grid gap-0.5">
              <span className="text-muted-foreground">Started by</span>
              <span className="truncate font-medium">
                {person(r.createdBy).name}
              </span>
            </span>
            <span className="grid gap-0.5">
              <span className="text-muted-foreground">Commit</span>
              <Code className="truncate text-xs text-foreground">
                {commitLabel(r) || "none"}
              </Code>
            </span>
            <span className="grid gap-0.5 text-right">
              <span className="text-muted-foreground">
                {r.status === "running"
                  ? "Running for"
                  : r.status === "waiting"
                    ? "Waiting for"
                    : "Finished"}
              </span>
              <span className="font-medium tabular-nums">
                {isLive(r)
                  ? duration(elapsed(r, now))
                  : relative(
                      Date.parse(r.finishedAt ?? r.createdAt) || started,
                      now
                    )}
              </span>
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}

function QuickAction({
  icon: Icon,
  label,
  badge,
  disabled,
  onClick,
}: {
  icon: LucideIcon
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

/** weekOf is the ISO week number of a date, the way the chart labels weeks. */
function weekOf(d: Date): number {
  const t = new Date(Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()))
  const day = t.getUTCDay() || 7
  t.setUTCDate(t.getUTCDate() + 4 - day)
  const jan1 = new Date(Date.UTC(t.getUTCFullYear(), 0, 1))
  return Math.ceil(((t.getTime() - jan1.getTime()) / 86_400_000 + 1) / 7)
}

/** weekly folds the activity summary's days into weeks, oldest first. */
function weekly(daily: { date: string; succeeded: number; failed: number }[]) {
  const out: { label: string; value: number }[] = []
  const sorted = [...daily].sort((a, b) => a.date.localeCompare(b.date))
  for (let i = 0; i < sorted.length; i += 7) {
    const chunk = sorted.slice(i, i + 7)
    const start = chunk[0]?.date ?? ""
    out.push({
      label: start ? `W${weekOf(new Date(`${start}T00:00:00`))}` : "",
      value: chunk.reduce((a, d) => a + d.succeeded + d.failed, 0),
    })
  }
  return out
}

function DeploysCard() {
  const navigate = useNavigate()
  const activity = useQuery(useActivityGet, { query: { days: daysShown } })
  const a = activity.data
  const weeks = a ? weekly(a.daily) : []
  const total = a ? a.deployments.succeeded + a.deployments.failed : 0
  const last = weeks.at(-1)?.value ?? 0
  const prev = weeks.at(-2)?.value ?? 0
  const change = prev ? Math.round(((last - prev) / prev) * 100) : undefined
  return (
    <MetricCard
      icon={RocketIcon}
      title="Deployments"
      className="lg:col-span-5"
      menu={[
        {
          label: "View all deployments",
          onSelect: () => void navigate({ to: "/deployments" }),
        },
        {
          label: "Open activity",
          onSelect: () => void navigate({ to: "/activity" }),
        },
      ]}
    >
      {activity.status === "error" ? (
        <QueryError
          error={activity.error}
          fallback="Deployment activity couldn't be loaded."
          retry={activity.refetch}
        />
      ) : !a ? (
        <Skeleton className="h-52 w-full" />
      ) : (
        <>
          <Figure
            value={total.toLocaleString()}
            delta={
              change === undefined
                ? undefined
                : `${change >= 0 ? "+" : ""}${change}%`
            }
            good={(change ?? 0) >= 0}
            note={`in ${daysShown / 7} weeks, ${last} this week${a.deployments.failed ? `, ${a.deployments.failed} failed` : ""}`}
          />
          <DotBarChart data={weeks} floor="zero" />
        </>
      )}
    </MetricCard>
  )
}

function CommitCard() {
  const activity = useQuery(useActivityGet, { query: { days: 30 } })
  const a = activity.data
  const median = a?.deployments.medianSeconds ?? 0
  const days = (a?.daily ?? [])
    .slice()
    .sort((x, y) => x.date.localeCompare(y.date))
    .slice(-12)
    .map((d) => ({
      label: new Date(`${d.date}T00:00:00`).toLocaleDateString(undefined, {
        day: "numeric",
      }),
      value: d.succeeded,
    }))
  return (
    <MetricCard icon={TimerIcon} title="Plan to live" className="lg:col-span-5">
      {activity.status === "error" ? (
        <QueryError
          error={activity.error}
          fallback="Deployment timing couldn't be loaded."
          retry={activity.refetch}
        />
      ) : !a ? (
        <Skeleton className="h-48 w-full" />
      ) : (
        <>
          <Figure
            value={median ? duration(median * 1000) : "None yet"}
            note={
              median
                ? "median from approval to live, last 30 days"
                : "Once deployments finish, their median time shows here."
            }
          />
          <GlowBarChart data={days} label="Went live" />
        </>
      )}
    </MetricCard>
  )
}

function QuickDeploy({
  runs,
  projects,
  projectsLoaded,
}: {
  runs: Run[]
  projects: { id: string; name: string; icon: string; color: string }[]
  projectsLoaded: boolean
}) {
  const { can } = useOrganization()
  const { openDeploy, openNewProject } = useShell()
  return (
    <MetricCard
      icon={ZapIcon}
      title="Quick deploy"
      className="lg:col-span-5"
      menu={
        can("project:create")
          ? [{ label: "New project", onSelect: openNewProject }]
          : undefined
      }
    >
      <div className="grid grid-cols-3 gap-2 sm:grid-cols-5">
        {!projectsLoaded &&
          projects.length === 0 &&
          [0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-[84px] rounded-xl" />
          ))}
        {projects.map((p) => {
          const latest = runs.find((r) => r.projectId === p.id)
          const s = projectStyle(p)
          return (
            <Button
              key={p.id}
              variant="outline"
              onClick={() => openDeploy({ project: p.id })}
              disabled={!can("deployment:create")}
              className="h-auto flex-col gap-2 rounded-xl px-2 py-3"
            >
              <span className="relative flex">
                <ProjectIcon icon={s.icon} color={s.color} size="lg" />
                {latest && (
                  <span
                    aria-hidden="true"
                    className={cn(
                      "absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-card",
                      latest.status === "failed" || latest.status === "blocked"
                        ? "bg-destructive"
                        : isLive(latest)
                          ? "bg-weld"
                          : "bg-overdye"
                    )}
                  />
                )}
              </span>
              <span className="w-full truncate">{p.name}</span>
            </Button>
          )
        })}
        {can("project:create") && (
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
        )}
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
  run: string
}

// The feed shows the latest few; the Activity page has the counts.
const eventsShown = 7

function useEvents(runs: Run[]) {
  const person = usePeople()
  return useMemo(() => {
    const out: Event[] = []
    const where = (r: Run) => (
      <>
        <span className="font-medium text-foreground">
          {r.project?.name ?? "a deleted project"}
        </span>{" "}
        to {r.environment}
      </>
    )
    const first = (id: string) => person(id).name.split(" ")[0]
    for (const r of runs.slice(0, 12)) {
      const ts = Date.parse(r.finishedAt ?? r.createdAt)
      const name = first(r.createdBy)
      const base = { run: r.id }
      if (r.status === "running") {
        out.push({
          ...base,
          id: `${r.id}-run`,
          ts: Date.parse(r.deployment?.startedAt ?? r.createdAt),
          icon: <PlayIcon />,
          tone: "bg-weld/15 text-weld-ink",
          text: (
            <>
              {r.deployment ? "Deploying" : "Planning"} {where(r)}
            </>
          ),
        })
      } else if (r.status === "waiting") {
        out.push({
          ...base,
          id: `${r.id}-wait`,
          ts: Date.parse(r.plan.plannedAt ?? r.createdAt),
          icon: <PauseIcon />,
          tone: "bg-weld/15 text-weld-ink",
          text: <>The plan for {where(r)} needs approval</>,
        })
      } else if (r.status === "failed" || r.status === "blocked") {
        out.push({
          ...base,
          id: `${r.id}-fail`,
          ts,
          icon: <XIcon />,
          tone: "bg-destructive/12 text-destructive",
          text: (
            <>
              {name}&rsquo;s deploy of {where(r)}{" "}
              {r.status === "blocked" ? "is blocked" : "failed"}
            </>
          ),
        })
      } else if (r.status === "canceled") {
        out.push({
          ...base,
          id: `${r.id}-cancel`,
          ts,
          icon: <RotateCcwIcon />,
          tone: "bg-muted text-muted-foreground",
          text: (
            <>
              The plan for {where(r)} was {r.plan.status}
            </>
          ),
        })
      } else if (r.status === "succeeded") {
        out.push({
          ...base,
          id: `${r.id}-ok`,
          ts,
          icon: <CheckIcon />,
          tone: "bg-overdye/12 text-overdye",
          text: (
            <>
              {name} deployed {where(r)}
            </>
          ),
        })
      }
      if (
        r.plan.decidedAt &&
        r.plan.decidedBy &&
        (r.deployment || r.plan.status === "approved")
      ) {
        out.push({
          ...base,
          id: `${r.id}-approve`,
          ts: Date.parse(r.plan.decidedAt),
          icon: <CheckIcon />,
          tone: "bg-muted text-foreground",
          text: (
            <>
              {first(r.plan.decidedBy)} approved the plan for {where(r)}
            </>
          ),
        })
      }
    }
    return out.sort((a, b) => b.ts - a.ts).slice(0, eventsShown)
  }, [runs, person])
}

function Feed({ runs, now }: { runs: Run[]; now: number }) {
  const navigate = useNavigate()
  const events = useEvents(runs)
  return (
    <MetricCard
      icon={ActivityIcon}
      title="Activity"
      className="lg:col-span-7"
      menu={[
        {
          label: "Open activity",
          onSelect: () => void navigate({ to: "/activity" }),
        },
        {
          label: "View all deployments",
          onSelect: () => void navigate({ to: "/deployments" }),
        },
      ]}
    >
      {events.length === 0 ? (
        <Empty className="p-0">
          <EmptyHeader>
            <EmptyTitle>No activity yet</EmptyTitle>
            <EmptyDescription>
              Plans, approvals and deployments appear here as they happen.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Link
              to="/deployments"
              className="text-sm underline underline-offset-4"
            >
              Open deployments
            </Link>
          </EmptyContent>
        </Empty>
      ) : (
        <ItemGroup className="gap-0.5">
          {events.map((e) => (
            <Item
              key={e.id}
              size="sm"
              className="-mx-3 py-2"
              render={<Link to="/deployments/$id" params={{ id: e.run }} />}
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
      )}
    </MetricCard>
  )
}

function Clouds() {
  const navigate = useNavigate()
  const connections = useQuery(useConnectionsList)
  const { bindings, error, refetch } = useRunning()
  const byConnection = useMemo(() => {
    const m = new Map<string, Binding[]>()
    for (const b of bindings) {
      m.set(b.connectionId, [...(m.get(b.connectionId) ?? []), b])
    }
    return m
  }, [bindings])
  const list = connections.data?.connections ?? []

  return (
    <MetricCard
      icon={CloudIcon}
      title="Clouds"
      className="lg:col-span-5"
      menu={[
        {
          label: "Open infrastructure",
          onSelect: () => void navigate({ to: "/infrastructure" }),
        },
      ]}
    >
      {connections.status === "error" ? (
        <QueryError
          error={connections.error}
          fallback="Cloud connections couldn't be loaded."
          retry={connections.refetch}
        />
      ) : error !== undefined ? (
        <QueryError
          error={error}
          fallback="Running services couldn't be loaded."
          retry={refetch}
        />
      ) : !connections.data ? (
        <Skeleton className="h-40 w-full" />
      ) : list.length === 0 ? (
        <Empty className="p-0">
          <EmptyHeader>
            <EmptyTitle>No clouds connected yet</EmptyTitle>
            <EmptyDescription>
              Connect Google Cloud, AWS, Vercel or Render to deploy there.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button
              variant="outline"
              size="sm"
              className="rounded-full"
              onClick={() => void navigate({ to: "/integrations" })}
            >
              Connect a cloud
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <ItemGroup className="gap-0.5">
          {list.map((c) => {
            const running = byConnection.get(c.id) ?? []
            const drifted = running.filter((b) => !!b.drift)
            return (
              <Item
                key={c.id}
                size="sm"
                className="-mx-3"
                render={<Link to="/infrastructure" />}
              >
                <ItemMedia>
                  <CloudGlyph
                    cloud={c.cloud}
                    className="size-8 rounded-lg text-[10px]"
                  />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle>
                    {c.name}
                    <span
                      className={cn(
                        "size-1.5 rounded-full",
                        c.status === "ready" ? "bg-overdye" : "bg-weld"
                      )}
                    />
                  </ItemTitle>
                  <ItemDescription className="line-clamp-1 text-xs">
                    {c.status === "ready"
                      ? `${cloudLabel(c.cloud)}, ${c.account}`
                      : c.message || "Needs attention"}
                  </ItemDescription>
                </ItemContent>
                <ItemActions className="grid justify-items-end gap-0.5 text-right">
                  <span className="text-sm tabular-nums">
                    {running.length}{" "}
                    <span className="text-muted-foreground">
                      {running.length === 1 ? "service" : "services"}
                    </span>
                  </span>
                  {drifted.length > 0 && (
                    <span className="text-weld-ink text-xs">
                      {drifted.length} drifted
                    </span>
                  )}
                </ItemActions>
              </Item>
            )
          })}
        </ItemGroup>
      )}
    </MetricCard>
  )
}
