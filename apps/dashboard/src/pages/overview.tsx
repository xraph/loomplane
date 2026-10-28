// The overview: what's waiting on you and what went out last, how often and
// how fast the organization ships, what it costs, how its services are
// doing, and where it runs. Spend, latency and traffic come from each
// connected cloud's own billing and monitoring: one card sums them up,
// and the traffic and slowest services cards beneath it show the detail.
import { useAuth } from "@authsome/ui-react"
import { CheckIcon, PlusIcon, ShieldCheckIcon } from "lucide-react"
import { type ReactNode, useCallback, useMemo } from "react"

import {
  useActivityGet,
  useConnectionsList,
} from "@/client/forge/loomplane/hooks"
import type { Binding } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { useDecision } from "@/components/approval"
import { CloudGlyph } from "@/components/cloud-glyph"
import { DotBarChart, GlowBarChart } from "@/components/dots"
import { Page, PageHeading } from "@/components/page"
import { Figure, Panel, PanelHead } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { ChangeChips, StatusPill, Thread } from "@/components/runs"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { LatencyCard, TrafficCard, UsageSummary } from "@/components/usage"
import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { cloudLabel } from "@/lib/clouds"
import { duration, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { commitLabel, isLive, type Run } from "@/lib/runs"
import { useNow, usePeople, useRunning, useRuns } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

// Twelve weeks of deploys, and two weeks of plan-to-live timing.
const weeks = 12
const daysShown = weeks * 7
const timingDays = 14

function greeting(now: number) {
  const h = new Date(now).getHours()
  if (h < 5) return "Working late"
  if (h < 12) return "Good morning"
  if (h < 18) return "Good afternoon"
  return "Good evening"
}

const counted = ["One", "Two", "Three", "Four", "Five"]

/** summary is the line under the greeting: what needs you, then the rest. */
export function summary(runs: Run[]): string {
  if (runs.length === 0)
    return "Plan your first deployment and it shows up here."
  const waiting = runs.filter((r) => r.status === "waiting").length
  const paused = runs.filter((r) => r.status === "paused").length
  const running = runs.filter((r) => r.status === "running").length
  // The latest finished run of each environment says whether it's live.
  const latest = new Map<string, Run>()
  for (const r of runs) {
    if (isLive(r)) continue
    const key = `${r.projectId}:${r.environment}`
    if (!latest.has(key)) latest.set(key, r)
  }
  const failing = [...latest.values()].filter(
    (r) => r.status === "failed" || r.status === "blocked"
  ).length
  const n = (k: number) => counted[k - 1] ?? String(k)
  const parts: string[] = []
  if (waiting)
    parts.push(
      `${n(waiting)} ${waiting === 1 ? "deploy is" : "deploys are"} waiting on you.`
    )
  if (paused)
    parts.push(
      `${n(paused)} ${paused === 1 ? "deploy is" : "deploys are"} paused for capacity.`
    )
  if (running)
    parts.push(`${n(running)} ${running === 1 ? "is" : "are"} deploying now.`)
  if (failing)
    parts.push(
      `${n(failing)} ${failing === 1 ? "environment's" : "environments'"} last deploy failed.`
    )
  else
    parts.push(
      parts.length ? "Everything else is live." : "Everything is live."
    )
  return parts.join(" ")
}

export function Overview() {
  const { user } = useAuth()
  const now = useNow()
  const { runs, projects, loaded, error, refetch } = useRuns()
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const waiting = runs
    .filter((r) => r.status === "waiting")
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))
  const release = runs.find((r) => r.status === "succeeded")
  const first = user?.first_name?.trim()
  return (
    <>
      <SiteHeader crumbs={[{ label: "Overview" }]} />
      <Page>
        <PageHeading
          title={first ? `${greeting(now)}, ${first}` : greeting(now)}
          description={loaded ? summary(runs) : "Loading your deployments…"}
          actions={
            can("deployment:create") && (
              <Button onClick={() => openDeploy()}>
                <PlusIcon />
                New deployment
                <Kbd className="ml-0.5 bg-primary-foreground/15 text-primary-foreground">
                  N
                </Kbd>
              </Button>
            )
          }
        />
        {error !== undefined && (
          <QueryError
            error={error}
            fallback="Some deployments couldn't be loaded."
            retry={refetch}
          />
        )}

        <div className="grid gap-3 lg:grid-cols-[minmax(0,10fr)_minmax(0,7fr)]">
          {!loaded && runs.length === 0 ? (
            <>
              <Skeleton className="h-[188px] rounded-[14px]" />
              <Skeleton className="h-[188px] rounded-[14px]" />
            </>
          ) : (
            <>
              <NeedsYou waiting={waiting} runs={runs} now={now} />
              <LastRelease run={release} now={now} />
            </>
          )}
        </div>

        <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)_minmax(0,1fr)]">
          <DeploysCard />
          <PlanToLive />
          <UsageSummary className="md:col-span-2 lg:col-span-1" />
        </div>

        <div className="grid gap-3 lg:grid-cols-[minmax(0,10fr)_minmax(0,7fr)]">
          <TrafficCard />
          <LatencyCard />
        </div>

        <div className="grid items-start gap-3 lg:grid-cols-[minmax(0,10fr)_minmax(0,7fr)]">
          <Feed runs={runs} loaded={loaded} now={now} />
          <div className="grid gap-3">
            <QuickDeploy projects={projects} loaded={loaded} />
            <Clouds />
          </div>
        </div>
      </Page>
    </>
  )
}

/* ------------------------------------------------------------------------ */
/* Shared card parts                                                          */

function Eyebrow({
  className,
  children,
}: {
  className?: string
  children: ReactNode
}) {
  return (
    <span
      className={cn(
        "text-xs font-semibold tracking-[0.06em] text-muted-foreground uppercase",
        className
      )}
    >
      {children}
    </span>
  )
}

/** useWho names a person the way the overview reads: you, or a first name. */
function useWho() {
  const { user } = useAuth()
  const person = usePeople()
  const me = user?.id
  return useCallback(
    (id: string, capital = true) =>
      me && id === me
        ? capital
          ? "You"
          : "you"
        : (person(id).name.split(" ")[0] ?? "Someone"),
    [me, person]
  )
}

/* ------------------------------------------------------------------------ */
/* What needs you, and what went out last                                     */

function NeedsYou({
  waiting,
  runs,
  now,
}: {
  waiting: Run[]
  runs: Run[]
  now: number
}) {
  const run = waiting[0]
  if (run) return <Waiting run={run} count={waiting.length} now={now} />
  const running = runs.filter((r) => r.status === "running")
  if (running[0]) return <InFlight run={running[0]} count={running.length} />
  return <AllClear />
}

function Waiting({
  run,
  count,
  now,
}: {
  run: Run
  count: number
  now: number
}) {
  const { can } = useOrganization()
  const { decide, busy, approving } = useDecision(run)
  const who = useWho()
  const p = run.plan
  return (
    <Panel
      aria-label="Waiting on you"
      className="gap-2.5 ring-[color-mix(in_oklch,var(--weld)_28%,var(--border))]"
    >
      <div className="flex items-baseline justify-between gap-3">
        <Eyebrow className="text-weld-ink">Waiting on you · {count}</Eyebrow>
        {p.expiresAt && (
          <span className="text-xs text-muted-foreground">
            Open until{" "}
            {new Date(p.expiresAt).toLocaleDateString(undefined, {
              month: "short",
              day: "numeric",
            })}
          </span>
        )}
      </div>
      <div className="flex min-w-0 gap-3">
        <span className="flex size-[34px] shrink-0 items-center justify-center rounded-lg bg-background text-weld-ink shadow-xs ring-1 ring-border">
          <ShieldCheckIcon className="size-4" />
        </span>
        <div className="grid min-w-0 gap-1">
          <span className="truncate text-[15px] font-semibold tracking-[-0.01em]">
            {run.project?.name ?? "A project"} to{" "}
            <span className="capitalize">{run.environment}</span>
          </span>
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px] text-muted-foreground">
            <ChangeChips run={run} />
            {p.warnings.length > 0 && (
              <span className="text-weld-ink">
                {p.warnings.length}{" "}
                {p.warnings.length === 1 ? "warning" : "warnings"}
              </span>
            )}
            <span>
              Planned {relative(Date.parse(run.createdAt), now)} by{" "}
              {who(run.createdBy, false)}
            </span>
          </span>
        </div>
      </div>
      <div className="mt-auto flex flex-wrap gap-2">
        <Button
          variant="outline"
          nativeButton={false}
          render={<Link to="/deployments/$id" params={{ id: run.id }} />}
        >
          Review plan
        </Button>
        {can("deployment:approve") && (
          <Button disabled={busy} onClick={() => void decide(true)}>
            {approving ? <Spinner /> : <CheckIcon />}
            {p.executable ? "Approve and deploy" : "Approve"}
          </Button>
        )}
      </div>
    </Panel>
  )
}

function InFlight({ run, count }: { run: Run; count: number }) {
  return (
    <Panel aria-label="In flight" className="gap-2.5">
      <Eyebrow className="text-flight-ink">In flight · {count}</Eyebrow>
      <div className="grid min-w-0 gap-1">
        <span className="truncate text-[15px] font-semibold tracking-[-0.01em]">
          {run.project?.name ?? "A project"} to{" "}
          <span className="capitalize">{run.environment}</span>
        </span>
        <span className="text-[12.5px] text-muted-foreground">{run.phase}</span>
      </div>
      <Thread steps={run.thread} />
      <div className="mt-auto flex flex-wrap gap-2">
        <Button
          variant="outline"
          nativeButton={false}
          render={<Link to="/deployments/$id" params={{ id: run.id }} />}
        >
          Watch deploy
        </Button>
      </div>
    </Panel>
  )
}

function AllClear() {
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  return (
    <Panel aria-label="Waiting on you" className="gap-2.5">
      <Eyebrow>Waiting on you · 0</Eyebrow>
      <div className="grid gap-1">
        <span className="text-[15px] font-semibold tracking-[-0.01em]">
          Nothing needs your approval
        </span>
        <span className="text-[12.5px] text-muted-foreground">
          Plans that change an environment wait here until someone approves
          them.
        </span>
      </div>
      {can("deployment:create") && (
        <div className="mt-auto">
          <Button variant="outline" onClick={() => openDeploy()}>
            Plan a deployment
          </Button>
        </div>
      )}
    </Panel>
  )
}

function LastRelease({ run, now }: { run?: Run; now: number }) {
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const who = useWho()
  const style = run?.project ? projectStyle(run.project) : null
  return (
    <Panel aria-label="Last release" className="gap-2.5">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Last release</Eyebrow>
        {run && <StatusPill tone="live">Live</StatusPill>}
      </div>
      {!run ? (
        <p className="text-[12.5px] text-muted-foreground">
          Nothing has gone live yet. Your first successful deploy shows here.
        </p>
      ) : (
        <>
          <div className="flex min-w-0 items-center gap-3">
            {style && (
              <ProjectIcon icon={style.icon} color={style.color} size="md" />
            )}
            <div className="grid min-w-0 gap-0.5">
              <span className="truncate text-[15px] font-semibold tracking-[-0.01em]">
                {run.project?.name ?? "A deleted project"}
              </span>
              <span className="flex flex-wrap items-center gap-x-3 text-[12.5px] text-muted-foreground">
                <span className="capitalize">{run.environment}</span>
                {commitLabel(run) && (
                  <span className="font-mono text-foreground">
                    {commitLabel(run)}
                  </span>
                )}
                <span>
                  Finished{" "}
                  {relative(Date.parse(run.finishedAt ?? run.createdAt), now)}{" "}
                  by {who(run.createdBy, false)}
                </span>
              </span>
            </div>
          </div>
          <div className="mt-auto flex flex-wrap gap-2">
            <Button
              variant="outline"
              nativeButton={false}
              render={<Link to="/deployments/$id" params={{ id: run.id }} />}
            >
              View deploy
            </Button>
            {can("deployment:create") && run.project && (
              <Button
                variant="outline"
                onClick={() =>
                  openDeploy({
                    project: run.projectId,
                    environment: run.environment,
                  })
                }
              >
                Redeploy
              </Button>
            )}
          </div>
        </>
      )}
    </Panel>
  )
}

/* ------------------------------------------------------------------------ */
/* How often and how fast                                                     */

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
  const activity = useQuery(useActivityGet, { query: { days: daysShown } })
  const a = activity.data
  const data = a ? weekly(a.daily) : []
  const total = a ? a.deployments.succeeded + a.deployments.failed : 0
  const thisWeek = data.at(-1)?.value ?? 0
  return (
    <Panel aria-label="Deployments" className="gap-2.5">
      <PanelHead
        title={
          <Link to="/deployments" className="hover:underline">
            Deployments
          </Link>
        }
        aside={`Last ${weeks} weeks`}
      />
      {activity.status === "error" ? (
        <QueryError
          error={activity.error}
          fallback="Deployment activity couldn't be loaded."
          retry={activity.refetch}
        />
      ) : !a ? (
        <Skeleton className="h-44 w-full" />
      ) : (
        <>
          <Figure
            value={total.toLocaleString()}
            note={`${thisWeek} this week${a.deployments.failed ? ` · ${a.deployments.failed} failed` : ""}`}
          />
          <DotBarChart data={data} floor="zero" />
        </>
      )}
    </Panel>
  )
}

function PlanToLive() {
  const activity = useQuery(useActivityGet, { query: { days: timingDays } })
  const a = activity.data
  const median = a?.deployments.medianSeconds ?? 0
  const days = (a?.daily ?? [])
    .slice()
    .sort((x, y) => x.date.localeCompare(y.date))
    .slice(-timingDays)
    .map((d) => ({
      label: new Date(`${d.date}T00:00:00`).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
      }),
      value: d.succeeded,
    }))
  return (
    <Panel aria-label="Plan to live" className="gap-2.5">
      <PanelHead title="Plan to live" aside={`Last ${timingDays} days`} />
      {activity.status === "error" ? (
        <QueryError
          error={activity.error}
          fallback="Deployment timing couldn't be loaded."
          retry={activity.refetch}
        />
      ) : !a ? (
        <Skeleton className="h-44 w-full" />
      ) : (
        <>
          <Figure
            value={median ? duration(median * 1000) : "None yet"}
            note={
              median
                ? "median, approval to live"
                : "Once deployments finish, their median shows here."
            }
          />
          <GlowBarChart data={days} label="Went live" ticks="ends" />
        </>
      )}
    </Panel>
  )
}

/* ------------------------------------------------------------------------ */
/* Activity, quick deploy and clouds                                          */

type Event = {
  id: string
  ts: number
  dot: string
  text: ReactNode
  run: string
}

// The feed shows the latest few; the Activity page has the rest.
const eventsShown = 6

function useEvents(runs: Run[]) {
  const who = useWho()
  return useMemo(() => {
    const out: Event[] = []
    const name = (r: Run) => (
      <span className="font-medium text-foreground">
        {r.project?.name ?? "a deleted project"}
      </span>
    )
    const actor = (s: string) => (
      <span className="font-medium text-foreground">{s}</span>
    )
    for (const r of runs.slice(0, 12)) {
      const ts = Date.parse(r.finishedAt ?? r.createdAt)
      const base = { run: r.id }
      const by = who(r.createdBy)
      if (r.status === "running") {
        out.push({
          ...base,
          id: `${r.id}-run`,
          ts: Date.parse(r.deployment?.startedAt ?? r.createdAt),
          dot: "bg-flight",
          text: (
            <>
              {actor(by)}{" "}
              {r.deployment ? "started deploying" : "started planning"}{" "}
              {name(r)} to {r.environment}
            </>
          ),
        })
      } else if (r.status === "waiting") {
        out.push({
          ...base,
          id: `${r.id}-wait`,
          ts: Date.parse(r.plan.plannedAt ?? r.createdAt),
          dot: "bg-weld",
          text: (
            <>
              {actor(by)} planned {name(r)} to {r.environment}. It needs
              approval.
            </>
          ),
        })
      } else if (r.status === "paused") {
        out.push({
          ...base,
          id: `${r.id}-pause`,
          ts: Date.parse(r.deployment?.capacityWait?.createdAt ?? r.createdAt),
          dot: "bg-weld",
          text: (
            <>
              The deploy of {name(r)} to {r.environment} paused for capacity
            </>
          ),
        })
      } else if (r.status === "blocked") {
        const n = r.plan.problems.length
        out.push({
          ...base,
          id: `${r.id}-block`,
          ts,
          dot: "bg-failed",
          text: (
            <>
              A plan for {name(r)} was blocked
              {n > 0 && ` by ${n} ${n === 1 ? "problem" : "problems"}`}
            </>
          ),
        })
      } else if (r.status === "failed") {
        out.push({
          ...base,
          id: `${r.id}-fail`,
          ts,
          dot: "bg-failed",
          text: (
            <>
              {actor(by === "You" ? "Your" : `${by}'s`)} deploy of {name(r)} to{" "}
              {r.environment} failed
            </>
          ),
        })
      } else if (r.status === "canceled") {
        out.push({
          ...base,
          id: `${r.id}-cancel`,
          ts,
          dot: "bg-muted-foreground/50",
          text:
            r.deployment?.status === "cancelled" ? (
              <>
                The deploy of {name(r)} to {r.environment} was canceled
              </>
            ) : (
              <>
                The plan for {name(r)} to {r.environment} was {r.plan.status}
              </>
            ),
        })
      } else if (r.status === "succeeded") {
        out.push({
          ...base,
          id: `${r.id}-ok`,
          ts,
          dot: "bg-overdye",
          text: (
            <>
              {actor(by)} deployed {name(r)} to {r.environment}
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
          dot: "bg-flight",
          text: (
            <>
              {actor(who(r.plan.decidedBy))} approved the plan for {name(r)} to{" "}
              {r.environment}
            </>
          ),
        })
      }
    }
    return out.sort((a, b) => b.ts - a.ts).slice(0, eventsShown)
  }, [runs, who])
}

function Feed({
  runs,
  loaded,
  now,
}: {
  runs: Run[]
  loaded: boolean
  now: number
}) {
  const events = useEvents(runs)
  return (
    <Panel aria-label="Activity" className="gap-0 p-0">
      <PanelHead
        className="border-b px-4 py-3"
        title="Activity"
        aside={
          <Link
            to="/activity"
            className="text-[12.5px] font-medium text-brand-ink hover:underline"
          >
            See all
          </Link>
        }
      />
      {!loaded && events.length === 0 ? (
        <div className="grid gap-3 px-4 py-3.5">
          <Skeleton className="h-5 w-3/4" />
          <Skeleton className="h-5 w-2/3" />
        </div>
      ) : events.length === 0 ? (
        <p className="px-4 py-3.5 text-[12.5px] text-muted-foreground">
          Plans, approvals and deployments appear here as they happen.
        </p>
      ) : (
        <ul className="divide-y">
          {events.map((e) => (
            <li key={e.id}>
              <Link
                to="/deployments/$id"
                params={{ id: e.run }}
                className="flex items-center gap-3 px-4 py-2.5 text-[13px] text-muted-foreground transition-colors hover:bg-subtle"
              >
                <span
                  aria-hidden="true"
                  className={cn("size-2 shrink-0 rounded-full", e.dot)}
                />
                <span className="min-w-0 flex-1 truncate">{e.text}</span>
                <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                  {relative(e.ts, now)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

function QuickDeploy({
  projects,
  loaded,
}: {
  projects: { id: string; name: string; icon: string; color: string }[]
  loaded: boolean
}) {
  const { can } = useOrganization()
  const { openDeploy, openNewProject } = useShell()
  return (
    <Panel aria-label="Quick deploy" className="gap-2.5 pt-3">
      <PanelHead title="Quick deploy" />
      <div className="flex flex-wrap gap-2">
        {!loaded &&
          projects.length === 0 &&
          [0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-8 w-36 rounded-lg" />
          ))}
        {projects.map((p) => {
          const s = projectStyle(p)
          return (
            <Button
              key={p.id}
              variant="outline"
              disabled={!can("deployment:create")}
              onClick={() => openDeploy({ project: p.id })}
              className="h-8 max-w-full gap-2 rounded-lg pr-2.5 pl-1.5 text-[12.5px]"
            >
              <ProjectIcon icon={s.icon} color={s.color} size="xs" />
              <span className="truncate">{p.name}</span>
            </Button>
          )
        })}
        {loaded && projects.length === 0 && can("project:create") && (
          <Button variant="outline" onClick={openNewProject}>
            <PlusIcon />
            New project
          </Button>
        )}
      </div>
    </Panel>
  )
}

function Clouds() {
  const connections = useQuery(useConnectionsList)
  const { bindings } = useRunning()
  const byConnection = useMemo(() => {
    const m = new Map<string, Binding[]>()
    for (const b of bindings) {
      m.set(b.connectionId, [...(m.get(b.connectionId) ?? []), b])
    }
    return m
  }, [bindings])
  const list = connections.data?.connections ?? []

  return (
    <Panel aria-label="Clouds" className="gap-0 p-0">
      <PanelHead
        className="border-b px-4 py-3"
        title={
          <>
            Clouds{" "}
            {list.length > 0 && (
              <span className="font-mono text-[11.5px] font-normal text-muted-foreground tabular-nums">
                {list.length}
              </span>
            )}
          </>
        }
        aside={
          <Link
            to="/clouds"
            className="text-[12.5px] font-medium text-brand-ink hover:underline"
          >
            Manage
          </Link>
        }
      />
      {connections.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={connections.error}
            fallback="Cloud connections couldn't be loaded."
            retry={connections.refetch}
          />
        </div>
      ) : !connections.data ? (
        <div className="p-4">
          <Skeleton className="h-24 w-full" />
        </div>
      ) : list.length === 0 ? (
        <div className="grid gap-3 px-4 py-3.5 text-[12.5px] text-muted-foreground">
          Connect Google Cloud, AWS, Vercel or Render to deploy there.
          <Link
            to="/integrations"
            className="w-fit font-medium text-brand-ink hover:underline"
          >
            Connect a cloud
          </Link>
        </div>
      ) : (
        <ul className="divide-y">
          {list.map((c) => {
            const running = byConnection.get(c.id)?.length ?? 0
            return (
              <li key={c.id}>
                <Link
                  to="/clouds/accounts/$id"
                  params={{ id: c.id }}
                  className="flex items-center gap-2.5 px-4 py-2 text-foreground transition-colors hover:bg-subtle"
                >
                  <CloudGlyph cloud={c.cloud} size="md" />
                  <span className="grid min-w-0 flex-1 gap-0.5">
                    <span className="truncate text-[13px] font-medium">
                      {c.name}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      {c.status === "ready"
                        ? [cloudLabel(c.cloud), c.account]
                            .filter(Boolean)
                            .join(" · ")
                        : c.message || "Needs attention"}
                    </span>
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                    {running
                      ? `${running} ${running === 1 ? "service" : "services"}`
                      : "No services"}
                  </span>
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}
