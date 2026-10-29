// Every deployment across the organization's projects, newest first, as
// runs: a plan and what came of it. One filter panel holds the status tabs,
// search, project, environment and time; the oldest plan waiting on you sits
// above it so it can be approved without opening it.
import { DeploymentsIcon, NoMatchIcon } from "@/components/icons"
import { ArrowFatLinesUpIcon } from "@phosphor-icons/react"
import {
  ChevronRightIcon,
  ClockIcon,
  GitCommitHorizontalIcon,
  LayersIcon,
  LayoutGridIcon,
  PlusIcon,
  SearchIcon,
  XIcon,
} from "lucide-react"
import { type ReactNode, useMemo, useState } from "react"

import { ApprovalCallout } from "@/components/approval"
import { PromoteDialog } from "@/components/promote-dialog"
import { QueryError } from "@/components/query-error"
import { Page, PageHeading } from "@/components/page"
import {
  ChangeChips,
  EnvBadge,
  PersonAvatar,
  StatusTile,
  Thread,
  toneClass,
  toneOf,
} from "@/components/runs"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Kbd } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { dateTime, duration, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import {
  commitLabel,
  elapsed,
  isLive,
  matches,
  type Run,
  runStatusText,
} from "@/lib/runs"
import { useNow, usePeople, useRuns } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** The status filters, in the URL as ?status=. */
export const runFilters = [
  { value: "all", label: "All", test: () => true },
  { value: "live", label: "In flight", test: isLive },
  {
    value: "waiting",
    label: "Needs approval",
    test: (r: Run) => r.status === "waiting",
  },
  {
    value: "failed",
    label: "Failed",
    test: (r: Run) => r.status === "failed" || r.status === "blocked",
  },
  {
    value: "succeeded",
    label: "Succeeded",
    test: (r: Run) => r.status === "succeeded",
  },
] as const

export type RunFilter = (typeof runFilters)[number]["value"]

// Enough rows to fill a screen or two before asking for more.
const pageSize = 25
const day = 86_400_000

const ranges = [
  { value: "any", label: "Any time", days: Infinity },
  { value: "1", label: "Last 24 hours", days: 1 },
  { value: "7", label: "Last 7 days", days: 7 },
  { value: "30", label: "Last 30 days", days: 30 },
] as const

type Range = (typeof ranges)[number]["value"]

function dayLabel(ts: number, now: number) {
  const midnight = (t: number) => new Date(t).setHours(0, 0, 0, 0)
  const diff = Math.round((midnight(now) - midnight(ts)) / day)
  if (diff === 0) return "Today"
  if (diff === 1) return "Yesterday"
  return new Date(ts).toLocaleDateString(undefined, {
    weekday: "long",
    month: "short",
    day: "numeric",
  })
}

// The middle value of a list of durations, or undefined for none.
function median(ms: number[]): number | undefined {
  if (ms.length === 0) return undefined
  const s = [...ms].sort((a, b) => a - b)
  const mid = Math.floor(s.length / 2)
  return s.length % 2 ? s[mid] : (s[mid - 1]! + s[mid]!) / 2
}

// The grid every row and the column headings share. On a phone a row keeps
// only its status, deployment and start; the steps move under the name.
const columns =
  "grid grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-3 md:grid-cols-[28px_minmax(0,1fr)_96px_minmax(0,150px)_120px_72px_116px_16px] md:gap-4"

export function Deployments({ status }: { status: RunFilter }) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const { runs, projects, loaded, error, refetch, whole, more, loadingMore } =
    useRuns()
  const now = useNow()
  const [projectID, setProjectID] = useState("all")
  const [env, setEnv] = useState("all")
  const [range, setRange] = useState<Range>("any")
  const [search, setSearch] = useState("")
  const [limit, setLimit] = useState(pageSize)
  const [promoting, setPromoting] = useState(false)

  const envs = useMemo(
    () => [...new Set(runs.map((r) => r.environment))].sort(),
    [runs]
  )
  // Past whole, another project's runs may not be read yet, so the list
  // stops there until more() reads further back.
  const known = useMemo(
    () =>
      whole === undefined
        ? runs
        : runs.filter((r) => Date.parse(r.createdAt) >= Date.parse(whole)),
    [runs, whole]
  )
  const days = ranges.find((r) => r.value === range)?.days ?? Infinity
  // Everything but the status tab narrows the counts, so a tab's count is
  // what clicking it shows.
  const scoped = useMemo(
    () =>
      known.filter(
        (r) =>
          (projectID === "all" || r.projectId === projectID) &&
          (env === "all" || r.environment === env) &&
          (days === Infinity || now - Date.parse(r.createdAt) <= days * day) &&
          matches(r, search)
      ),
    [known, projectID, env, days, now, search]
  )
  const filter = runFilters.find((f) => f.value === status) ?? runFilters[0]
  const rows = scoped.filter((r) => filter.test(r))
  const older = whole !== undefined
  const showMore = () => {
    setLimit((l) => l + pageSize)
    // Read further back when what's loaded won't fill the next page.
    if (older && rows.length < limit + pageSize) more()
  }
  const groups: { label: string; rows: Run[] }[] = []
  for (const r of rows.slice(0, limit)) {
    const label = dayLabel(Date.parse(r.createdAt), now)
    const last = groups.at(-1)
    if (last?.label === label) last.rows.push(r)
    else groups.push({ label, rows: [r] })
  }

  const waiting = known
    .filter((r) => r.status === "waiting")
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))
  const succeeded = scoped.filter((r) => r.status === "succeeded")
  const finished = scoped.filter(
    (r) => r.status === "succeeded" || r.status === "failed"
  )
  const typical = median(succeeded.map((r) => elapsed(r, now)))

  const setStatus = (v: string) =>
    void navigate({
      to: "/deployments",
      search: v === "all" ? {} : { status: v as RunFilter },
    })
  const clear = () => {
    setStatus("all")
    setProjectID("all")
    setEnv("all")
    setRange("any")
    setSearch("")
  }
  const projectName = projects.find((p) => p.id === projectID)?.name
  const chips: {
    key: string
    label: string
    value: string
    clear: () => void
  }[] = [
    ...(search.trim()
      ? [
          {
            key: "q",
            label: "Search",
            value: `“${search.trim()}”`,
            clear: () => setSearch(""),
          },
        ]
      : []),
    ...(projectName
      ? [
          {
            key: "p",
            label: "Project",
            value: projectName,
            clear: () => setProjectID("all"),
          },
        ]
      : []),
    ...(env !== "all"
      ? [
          {
            key: "e",
            label: "Environment",
            value: env,
            clear: () => setEnv("all"),
          },
        ]
      : []),
    ...(range !== "any"
      ? [
          {
            key: "t",
            label: "Time",
            value: ranges.find((r) => r.value === range)?.label ?? "",
            clear: () => setRange("any"),
          },
        ]
      : []),
  ]

  return (
    <>
      <SiteHeader crumbs={[{ label: "Deployments" }]} />
      <Page>
        <PageHeading
          title="Deployments"
          description="Every deploy across your projects, from plan to live. Search by plan ID, commit, repository or project."
          actions={
            can("deployment:create") && (
              <>
                <Button variant="outline" onClick={() => setPromoting(true)}>
                  <ArrowFatLinesUpIcon aria-hidden="true" />
                  Promote
                </Button>
                <Button onClick={() => openDeploy()}>
                  <PlusIcon />
                  New deployment
                  <Kbd className="ml-0.5 bg-primary-foreground/15 text-primary-foreground">
                    N
                  </Kbd>
                </Button>
              </>
            )
          }
        />
        {can("deployment:create") && (
          <PromoteDialog open={promoting} onOpenChange={setPromoting} />
        )}

        {waiting[0] && (
          <ApprovalCallout
            run={waiting[0]}
            waiting={waiting.length}
            now={now}
          />
        )}

        <section
          aria-label="Filter deployments"
          className="rounded-xl bg-card ring-1 ring-border"
        >
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-b pr-4 pl-2">
            <div role="tablist" aria-label="Status" className="flex flex-wrap">
              {runFilters.map((f) => {
                const on = f.value === filter.value
                return (
                  <button
                    key={f.value}
                    type="button"
                    role="tab"
                    aria-selected={on}
                    onClick={() => setStatus(f.value)}
                    className={cn(
                      "-mb-px flex h-10 items-center gap-1.5 px-3 text-[13.5px] font-medium transition-colors",
                      on
                        ? "text-foreground shadow-[inset_0_-2px_0_var(--primary)]"
                        : "text-muted-foreground hover:text-foreground"
                    )}
                  >
                    {f.label}
                    <span
                      className={cn(
                        "min-w-[18px] rounded-full px-1.5 text-center font-mono text-[11px] tabular-nums",
                        on
                          ? "bg-primary text-primary-foreground"
                          : "bg-muted text-muted-foreground"
                      )}
                    >
                      {scoped.filter((r) => f.test(r)).length}
                    </span>
                  </button>
                )
              })}
            </div>
            <div className="flex flex-wrap gap-x-4 py-2 text-[12.5px] text-muted-foreground">
              <span>
                <span className="font-mono text-foreground tabular-nums">
                  {scoped.length}
                </span>{" "}
                deploys
              </span>
              {finished.length > 0 && (
                <span>
                  <span className="font-mono text-overdye-ink tabular-nums">
                    {Math.round((succeeded.length / finished.length) * 100)}%
                  </span>{" "}
                  succeeded
                </span>
              )}
              {typical !== undefined && (
                <span>
                  <span className="font-mono text-foreground tabular-nums">
                    {duration(typical)}
                  </span>{" "}
                  median to live
                </span>
              )}
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2 p-2.5">
            <label className="relative flex min-w-[240px] flex-[1_1_300px] items-center">
              <SearchIcon className="pointer-events-none absolute left-3 size-4 text-muted-foreground" />
              <span className="sr-only">Search deployments</span>
              <input
                data-page-search
                type="search"
                placeholder="Search by plan ID, commit, repository or project"
                aria-label="Search deployments"
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value)
                  setLimit(pageSize)
                }}
                className="h-9 w-full rounded-[10px] border border-transparent bg-muted pr-10 pl-9 text-[13.5px] outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
              />
              <Kbd className="pointer-events-none absolute right-2.5">/</Kbd>
            </label>
            <FilterSelect
              icon={<LayoutGridIcon />}
              label="Project"
              value={projectID}
              onChange={setProjectID}
              items={[
                { value: "all", label: "All projects" },
                ...projects.map((p) => ({ value: p.id, label: p.name })),
              ]}
              render={(value) => {
                const p = projects.find((x) => x.id === value)
                if (!p) return undefined
                const s = projectStyle(p)
                return <ProjectIcon icon={s.icon} color={s.color} size="xs" />
              }}
            />
            <FilterSelect
              icon={<LayersIcon />}
              label="Environment"
              value={env}
              onChange={setEnv}
              items={[
                { value: "all", label: "All environments" },
                ...envs.map((e) => ({ value: e, label: e })),
              ]}
            />
            <FilterSelect
              icon={<ClockIcon />}
              label="Time range"
              value={range}
              onChange={(v) => setRange(v as Range)}
              items={ranges.map((r) => ({ value: r.value, label: r.label }))}
              neutral="any"
            />
          </div>
          {chips.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5 px-3 pb-3">
              <span className="mr-0.5 text-xs text-muted-foreground">
                Filtered by
              </span>
              {chips.map((c) => (
                <button
                  key={c.key}
                  type="button"
                  onClick={c.clear}
                  aria-label={`Remove the ${c.label.toLowerCase()} filter`}
                  className="flex h-7 items-center gap-1.5 rounded-full bg-brand-soft pr-2 pl-2.5 text-[12.5px] ring-1 ring-brand-line hover:bg-brand-tint"
                >
                  <span className="text-muted-foreground">{c.label}</span>
                  <span className="font-medium">{c.value}</span>
                  <XIcon className="size-3 text-muted-foreground" />
                </button>
              ))}
              <Button
                variant="ghost"
                size="sm"
                className="h-7 text-brand-ink"
                onClick={clear}
              >
                Clear all
              </Button>
            </div>
          )}
        </section>

        {error !== undefined && (
          <QueryError
            error={error}
            fallback="Some deployments couldn't be loaded."
            retry={refetch}
          />
        )}

        {!loaded && runs.length === 0 ? (
          <div className="grid gap-3" aria-busy="true">
            <Skeleton className="h-5 w-24" />
            <Skeleton className="h-64 rounded-xl" />
          </div>
        ) : error !== undefined && runs.length === 0 ? null : runs.length ===
          0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <DeploymentsIcon />
              </EmptyMedia>
              <EmptyTitle>No deployments yet</EmptyTitle>
              <EmptyDescription>
                A deployment starts as a plan of one environment. Plan one, and
                it shows up here from the first step to live.
              </EmptyDescription>
            </EmptyHeader>
            {can("deployment:create") && (
              <EmptyContent>
                <Button onClick={() => openDeploy()}>New deployment</Button>
              </EmptyContent>
            )}
          </Empty>
        ) : rows.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <NoMatchIcon />
              </EmptyMedia>
              <EmptyTitle>No deployments match</EmptyTitle>
              <EmptyDescription>
                {older
                  ? "Nothing loaded so far fits those filters. Older deployments may, or clear the filters to see everything again."
                  : "Nothing fits those filters. Clear them to see everything again."}
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button variant="outline" size="sm" onClick={clear}>
                Clear filters
              </Button>
              {older && (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={loadingMore}
                  onClick={more}
                >
                  {loadingMore ? "Loading…" : "Look further back"}
                </Button>
              )}
            </EmptyContent>
          </Empty>
        ) : (
          <div className="-mx-1 overflow-x-auto px-1">
            <div className="grid gap-4 md:min-w-[880px]">
              <div
                aria-hidden="true"
                className={cn(
                  columns,
                  "-mb-2 px-[17px] text-[11px] font-medium tracking-[0.04em] text-muted-foreground uppercase max-md:hidden"
                )}
              >
                <span />
                <span>Deployment</span>
                <span>Commit</span>
                <span>Changes</span>
                <span>Steps</span>
                <span className="text-right">Took</span>
                <span className="text-right">Started</span>
                <span />
              </div>
              {groups.map((g) => (
                <section key={g.label} className="grid gap-1.5">
                  <h2 className="flex items-baseline gap-2 px-1 text-[13px] font-semibold">
                    {g.label}
                    <span className="font-mono text-[11.5px] font-normal text-muted-foreground tabular-nums">
                      {g.rows.length}
                    </span>
                  </h2>
                  <div className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
                    {g.rows.map((r) => (
                      <RunRow key={r.id} run={r} now={now} />
                    ))}
                  </div>
                </section>
              ))}
              <div className="flex items-center justify-between px-1 text-[12.5px] text-muted-foreground">
                <span className="tabular-nums">
                  {older
                    ? `Showing ${Math.min(limit, rows.length)}, with older ones still to load`
                    : `Showing ${Math.min(limit, rows.length)} of ${rows.length}`}
                </span>
                {(rows.length > limit || older) && (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={loadingMore}
                    onClick={showMore}
                  >
                    {loadingMore ? "Loading…" : "Show more"}
                  </Button>
                )}
              </div>
            </div>
          </div>
        )}
      </Page>
    </>
  )
}

/** FilterSelect is one of the filter panel's dropdowns, tinted when set. */
function FilterSelect({
  icon,
  label,
  value,
  onChange,
  items,
  neutral = "all",
  render,
}: {
  icon: ReactNode
  label: string
  value: string
  onChange: (v: string) => void
  items: { value: string; label: string }[]
  /** The value that means "no filter". */
  neutral?: string
  /** A leading mark for an item, such as a project's tile. */
  render?: (value: string) => ReactNode
}) {
  const on = value !== neutral
  return (
    <Select
      items={items}
      value={value}
      onValueChange={(v) => onChange(String(v))}
    >
      <SelectTrigger
        aria-label={label}
        className={cn(
          "h-9 max-w-60 gap-2 rounded-[10px] [&>svg:first-child]:text-muted-foreground",
          on && "bg-brand-soft font-medium ring-1 ring-brand-line"
        )}
      >
        {icon}
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {items.map((it) => (
          <SelectItem key={it.value} value={it.value}>
            {render?.(it.value)}
            {it.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function RunRow({ run: r, now }: { run: Run; now: number }) {
  const person = usePeople()
  const author = person(r.createdBy)
  const style = r.project ? projectStyle(r.project) : null
  const started = Date.parse(r.createdAt)
  const tone = toneClass[toneOf(r.status)]
  return (
    <Link
      to="/deployments/$id"
      params={{ id: r.id }}
      className={cn(
        columns,
        "group px-4 py-2.5 text-foreground transition-colors hover:bg-subtle"
      )}
    >
      <StatusTile status={r.status} />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex min-w-0 items-center gap-2">
          {style && (
            <ProjectIcon icon={style.icon} color={style.color} size="xs" />
          )}
          <span className="min-w-0 truncate text-sm font-medium">
            {r.project?.name ?? "A deleted project"}
          </span>
          <EnvBadge env={r.environment} />
        </span>
        <span className={cn("truncate text-[12.5px]", tone.ink)}>
          {r.status === "running" ? r.phase : runStatusText[r.status]}
        </span>
        <span className="mt-1 md:hidden">
          <Thread steps={r.thread} size="sm" />
        </span>
      </span>
      <span className="flex items-center gap-1.5 font-mono text-[12.5px] text-muted-foreground max-md:hidden">
        <GitCommitHorizontalIcon className="size-3.5 shrink-0" />
        <span className="truncate">{commitLabel(r) || "no commit"}</span>
      </span>
      <span className="min-w-0 max-md:hidden">
        <ChangeChips run={r} />
      </span>
      <span className="max-md:hidden">
        <Thread steps={r.thread} size="sm" />
      </span>
      <span
        className={cn(
          "text-right font-mono text-[12.5px] tabular-nums max-md:hidden",
          isLive(r) ? "text-foreground" : "text-muted-foreground"
        )}
      >
        {duration(elapsed(r, now))}
      </span>
      <span
        className="flex items-center justify-end gap-2 text-[12.5px] text-muted-foreground"
        title={dateTime(started)}
      >
        <PersonAvatar initials={author.initials} name={author.name} />
        <span className="whitespace-nowrap">{relative(started, now)}</span>
        <span className="sr-only">by {author.name}</span>
      </span>
      <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5 group-hover:text-foreground max-md:hidden" />
    </Link>
  )
}
