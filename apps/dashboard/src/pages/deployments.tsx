// Every deployment across the organization's projects, newest first, as
// runs: a plan and what came of it. Filters by status, project and
// environment; search finds a run by ID, commit, repository or project.
import { ChevronRightIcon, PlusIcon, SearchIcon } from "lucide-react"
import { Fragment, useMemo, useState } from "react"

import { QueryError } from "@/components/query-error"
import { Page, PageHeading } from "@/components/page"
import {
  Code,
  EnvBadge,
  PersonAvatar,
  RunDot,
  segmented,
  Thread,
} from "@/components/runs"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item"
import { Kbd } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
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

export function Deployments({ status }: { status: RunFilter }) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const { runs, projects, loaded, error, refetch } = useRuns()
  const now = useNow()
  const [projectID, setProjectID] = useState("all")
  const [env, setEnv] = useState("all")
  const [search, setSearch] = useState("")
  const [limit, setLimit] = useState(pageSize)

  const envs = useMemo(
    () => [...new Set(runs.map((r) => r.environment))].sort(),
    [runs]
  )
  const scoped = useMemo(
    () =>
      runs.filter(
        (r) =>
          (projectID === "all" || r.projectId === projectID) &&
          (env === "all" || r.environment === env) &&
          matches(r, search)
      ),
    [runs, projectID, env, search]
  )
  const filter = runFilters.find((f) => f.value === status) ?? runFilters[0]
  const rows = scoped.filter((r) => filter.test(r))
  const groups: { label: string; rows: Run[] }[] = []
  for (const r of rows.slice(0, limit)) {
    const label = dayLabel(Date.parse(r.createdAt), now)
    const last = groups.at(-1)
    if (last?.label === label) last.rows.push(r)
    else groups.push({ label, rows: [r] })
  }

  const setStatus = (v: string) =>
    void navigate({
      to: "/deployments",
      search: v === "all" ? {} : { status: v as RunFilter },
    })

  return (
    <>
      <SiteHeader crumbs={[{ label: "Deployments" }]} />
      <Page className="gap-6">
        <PageHeading
          title="Deployments"
          description="Every deploy across your projects, from plan to live. Search by plan ID, commit, repository or project."
          actions={
            can("deployment:create") && (
              <Button className="rounded-full" onClick={() => openDeploy()}>
                <PlusIcon />
                New deployment
              </Button>
            )
          }
        />
        <div className="sticky top-12 z-10 -mx-4 flex flex-wrap items-center gap-2 border-b bg-background/85 px-4 py-3 backdrop-blur-xl md:-mx-8 md:px-8 lg:-mx-10 lg:px-10">
          <InputGroup className="h-8 w-full rounded-full sm:w-72">
            <InputGroupAddon>
              <SearchIcon />
            </InputGroupAddon>
            <InputGroupInput
              data-page-search
              placeholder="Search deployments"
              value={search}
              onChange={(e) => {
                setSearch(e.target.value)
                setLimit(pageSize)
              }}
              aria-label="Search deployments"
            />
            <InputGroupAddon align="inline-end">
              <Kbd>/</Kbd>
            </InputGroupAddon>
          </InputGroup>
          <ToggleGroup
            variant="outline"
            size="sm"
            spacing={0}
            className="max-w-full overflow-x-auto"
            value={[filter.value]}
            onValueChange={(v) => v[0] && setStatus(v[0])}
            aria-label="Status"
          >
            {runFilters.map((f) => (
              <ToggleGroupItem
                key={f.value}
                value={f.value}
                className={cn(segmented, "gap-1.5")}
              >
                {f.label}
                <span className="text-muted-foreground tabular-nums">
                  {scoped.filter((r) => f.test(r)).length}
                </span>
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <div className="flex gap-2 sm:ml-auto">
            <Select
              items={[
                { label: "All projects", value: "all" },
                ...projects.map((p) => ({ label: p.name, value: p.id })),
              ]}
              value={projectID}
              onValueChange={(v) => setProjectID(String(v))}
            >
              <SelectTrigger
                size="sm"
                aria-label="Project"
                className="w-40 rounded-full"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All projects</SelectItem>
                {projects.map((p) => {
                  const s = projectStyle(p)
                  return (
                    <SelectItem key={p.id} value={p.id}>
                      <ProjectIcon icon={s.icon} color={s.color} size="xs" />
                      {p.name}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
            <Select
              items={[
                { label: "All environments", value: "all" },
                ...envs.map((e) => ({ label: e, value: e })),
              ]}
              value={env}
              onValueChange={(v) => setEnv(String(v))}
            >
              <SelectTrigger
                size="sm"
                aria-label="Environment"
                className="w-44 rounded-full"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All environments</SelectItem>
                {envs.map((e) => (
                  <SelectItem key={e} value={e}>
                    {e}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

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
          <Empty className="border border-dashed">
            <EmptyHeader>
              <EmptyTitle>No deployments yet</EmptyTitle>
              <EmptyDescription>
                A deployment starts as a plan of one environment. Plan one, and
                it shows up here from the first step to live.
              </EmptyDescription>
            </EmptyHeader>
            {can("deployment:create") && (
              <EmptyContent>
                <Button className="rounded-full" onClick={() => openDeploy()}>
                  New deployment
                </Button>
              </EmptyContent>
            )}
          </Empty>
        ) : rows.length === 0 ? (
          <Empty className="border border-dashed">
            <EmptyHeader>
              <EmptyTitle>No deployments match</EmptyTitle>
              <EmptyDescription>
                Nothing fits those filters. Clear them to see everything again.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button
                variant="outline"
                size="sm"
                className="rounded-full"
                onClick={() => {
                  setStatus("all")
                  setProjectID("all")
                  setEnv("all")
                  setSearch("")
                }}
              >
                Clear filters
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <div className="grid gap-8">
            {groups.map((g) => (
              <section key={g.label} className="grid gap-3">
                <h2 className="flex items-baseline gap-2 text-sm font-medium">
                  {g.label}
                  <span className="text-xs font-normal text-muted-foreground tabular-nums">
                    {g.rows.length}
                  </span>
                </h2>
                <Card className="gap-0 py-0">
                  <ItemGroup className="gap-0">
                    {g.rows.map((r, i) => (
                      <Fragment key={r.id}>
                        {i > 0 && <ItemSeparator className="my-0" />}
                        <RunRow run={r} now={now} />
                      </Fragment>
                    ))}
                  </ItemGroup>
                </Card>
              </section>
            ))}
            <div className="flex items-center justify-between text-sm text-muted-foreground">
              <span className="tabular-nums">
                Showing {Math.min(limit, rows.length)} of {rows.length}
              </span>
              {rows.length > limit && (
                <Button
                  variant="outline"
                  size="sm"
                  className="rounded-full"
                  onClick={() => setLimit((l) => l + pageSize)}
                >
                  Show more
                </Button>
              )}
            </div>
          </div>
        )}
      </Page>
    </>
  )
}

/** changesLine says in a few words what a run's plan does. */
export function changesLine(r: Run): string {
  const p = r.plan
  if (p.status === "planning") return "Working out the plan"
  if (p.status === "failed") return "Planning failed"
  if (p.problems.length > 0) {
    return `${p.problems.length} ${p.problems.length === 1 ? "problem" : "problems"} to fix`
  }
  const s = p.summary
  const parts = [
    s.create && `${s.create} to create`,
    s.update && `${s.update} to update`,
    s.replace && `${s.replace} to replace`,
    s.delete && `${s.delete} to delete`,
  ].filter(Boolean)
  return parts.length ? parts.join(", ") : "Nothing to change"
}

export function RunRow({ run: r, now }: { run: Run; now: number }) {
  const person = usePeople()
  const author = person(r.createdBy)
  const style = r.project ? projectStyle(r.project) : null
  const started = Date.parse(r.createdAt)
  return (
    <Item
      render={<Link to="/deployments/$id" params={{ id: r.id }} />}
      className="group flex-nowrap gap-4 rounded-none px-4 py-3.5 md:px-5"
    >
      <ItemMedia>
        <Tooltip>
          <TooltipTrigger render={<span className="flex py-1" />}>
            <RunDot status={r.status} />
          </TooltipTrigger>
          <TooltipContent>{runStatusText[r.status]}</TooltipContent>
        </Tooltip>
      </ItemMedia>
      <ItemContent className="min-w-0 md:max-w-56 md:flex-none md:basis-56">
        <ItemTitle className="w-full min-w-0">
          {style && (
            <ProjectIcon icon={style.icon} color={style.color} size="xs" />
          )}
          <span className="min-w-0 truncate">
            {r.project?.name ?? "A deleted project"}
          </span>
          <EnvBadge env={r.environment} />
        </ItemTitle>
        <ItemDescription className="truncate md:hidden">
          {r.phase}
        </ItemDescription>
      </ItemContent>
      <ItemContent className="hidden min-w-0 flex-1! md:flex">
        <ItemTitle className="w-full min-w-0 font-normal">
          <span className="min-w-0 truncate">{r.phase}</span>
        </ItemTitle>
        <ItemDescription className="flex min-w-0 items-center gap-2 text-xs">
          <Code>{commitLabel(r) || "no commit"}</Code>
          <span className="truncate">{changesLine(r)}</span>
        </ItemDescription>
      </ItemContent>
      <ItemActions className="shrink-0 gap-4">
        <span className="hidden w-44 lg:block">
          <Thread steps={r.thread} size="sm" />
        </span>
        <span
          className={cn(
            "hidden w-16 text-right text-sm tabular-nums md:block",
            isLive(r) ? "text-foreground" : "text-muted-foreground"
          )}
        >
          {duration(elapsed(r, now))}
        </span>
        <span
          className="flex items-center justify-end gap-2 text-sm text-muted-foreground"
          title={dateTime(started)}
        >
          <PersonAvatar initials={author.initials} name={author.name} />
          <span className="hidden w-16 text-right whitespace-nowrap sm:inline">
            {relative(started, now)}
          </span>
          <span className="sr-only">by {author.name}</span>
        </span>
        <ChevronRightIcon className="hidden size-4 text-muted-foreground/50 transition-transform group-hover:translate-x-0.5 group-hover:text-foreground md:block" />
      </ItemActions>
    </Item>
  )
}
