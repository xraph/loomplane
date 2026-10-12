import { ChevronRightIcon, GitBranchIcon, SearchIcon } from "lucide-react"
import { Fragment, useMemo, useState } from "react"

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
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import {
  Code,
  EnvBadge,
  PersonAvatar,
  segmented,
  StatusDot,
  statusText,
} from "@/mock/components/bits"
import { LoomThread } from "@/mock/components/loom"
import { Page, PageHeading } from "@/components/page"
import { TopBar } from "@/mock/components/top-bar"
import { type Env, organization, person, type Project } from "@/mock/data"
import { ProjectIcon } from "@/lib/project-style"
import { dateTime, duration, relative } from "@/mock/format"
import { navigate, to, useQuery } from "@/mock/router"
import { type DeploymentView, isLive, useStore } from "@/mock/store"

const filters: {
  value: string
  label: string
  test: (d: DeploymentView) => boolean
}[] = [
  { value: "all", label: "All", test: () => true },
  { value: "live", label: "In flight", test: isLive },
  {
    value: "waiting",
    label: "Needs approval",
    test: (d) => d.status === "waiting",
  },
  { value: "failed", label: "Failed", test: (d) => d.status === "failed" },
  {
    value: "succeeded",
    label: "Succeeded",
    test: (d) => d.status === "succeeded",
  },
]

const envs: Env[] = ["production", "staging", "preview"]
const pageSize = 25

function dayLabel(ts: number, now: number) {
  const midnight = (t: number) => new Date(t).setHours(0, 0, 0, 0)
  const diff = Math.round((midnight(now) - midnight(ts)) / 86_400_000)
  if (diff === 0) return "Today"
  if (diff === 1) return "Yesterday"
  return new Date(ts).toLocaleDateString(undefined, {
    weekday: "long",
    month: "short",
    day: "numeric",
  })
}

export function DeploymentsPage() {
  const { deployments, now, projects } = useStore()
  const query = useQuery()
  const status = query.get("status") ?? "all"
  const [projectId, setProjectId] = useState("all")
  const [env, setEnv] = useState("all")
  const [search, setSearch] = useState("")
  const [limit, setLimit] = useState(pageSize)

  const scoped = useMemo(
    () =>
      deployments.filter(
        (d) =>
          (projectId === "all" || d.project === projectId) &&
          (env === "all" || d.env === env) &&
          (!search ||
            `${d.id} ${d.sha} ${d.message} ${d.branch} ${d.project}`
              .toLowerCase()
              .includes(search.toLowerCase()))
      ),
    [deployments, projectId, env, search]
  )
  const test = filters.find((f) => f.value === status)?.test ?? (() => true)
  const rows = scoped.filter(test)
  const groups: { label: string; rows: DeploymentView[] }[] = []
  for (const d of rows.slice(0, limit)) {
    const label = dayLabel(d.startedAt, now)
    const last = groups.at(-1)
    if (last?.label === label) last.rows.push(d)
    else groups.push({ label, rows: [d] })
  }

  const setStatus = (v: string) =>
    navigate(v === "all" ? "/deployments" : `/deployments?status=${v}`)

  return (
    <>
      <TopBar
        crumbs={[
          { label: organization.name, path: "/" },
          { label: "Deployments" },
        ]}
      />
      <Page className="gap-6">
        <PageHeading
          title="Deployments"
          description="Every deploy across your projects. Search by SHA, branch, commit message or deployment ID."
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
            value={[status]}
            onValueChange={(v) => v[0] && setStatus(v[0])}
            aria-label="Status"
          >
            {filters.map((f) => (
              <ToggleGroupItem
                key={f.value}
                value={f.value}
                className={cn(segmented, "gap-1.5")}
              >
                {f.label}
                <span className="text-muted-foreground tabular-nums">
                  {scoped.filter(f.test).length}
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
              value={projectId}
              onValueChange={(v) => setProjectId(String(v))}
            >
              <SelectTrigger
                size="sm"
                aria-label="Project"
                className="w-36 rounded-full"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All projects</SelectItem>
                {projects.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    <ProjectIcon icon={p.icon} color={p.color} size="xs" />
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              items={[
                { label: "All environments", value: "all" },
                ...envs.map((e) => ({
                  label: e[0]?.toUpperCase() + e.slice(1),
                  value: e,
                })),
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
                  <SelectItem key={e} value={e} className="capitalize">
                    {e}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        {rows.length === 0 ? (
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
                  setProjectId("all")
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
                    {g.rows.map((d, i) => (
                      <Fragment key={d.id}>
                        {i > 0 && <ItemSeparator className="my-0" />}
                        <Row
                          d={d}
                          now={now}
                          project={projects.find((p) => p.id === d.project)}
                        />
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

function Row({
  d,
  now,
  project,
}: {
  d: DeploymentView
  now: number
  project?: Project
}) {
  return (
    <Item
      render={<a href={to(`/deployments/${d.id}`)} />}
      className="group flex-nowrap gap-4 rounded-none px-4 py-3.5 md:px-5"
    >
      <ItemMedia>
        <Tooltip>
          <TooltipTrigger render={<span className="flex py-1" />}>
            <StatusDot status={d.status} />
          </TooltipTrigger>
          <TooltipContent>{statusText[d.status]}</TooltipContent>
        </Tooltip>
      </ItemMedia>
      <ItemContent className="min-w-0 md:max-w-52 md:flex-none md:basis-52">
        <ItemTitle className="w-full min-w-0">
          {project && (
            <ProjectIcon icon={project.icon} color={project.color} size="xs" />
          )}
          <span className="min-w-0 truncate">{d.project}</span>
          <EnvBadge env={d.env} />
        </ItemTitle>
        <ItemDescription className="truncate md:hidden">
          {d.message}
        </ItemDescription>
      </ItemContent>
      <ItemContent className="hidden min-w-0 flex-1! md:flex">
        <ItemTitle className="w-full min-w-0 font-normal">
          <span className="min-w-0 truncate">{d.message}</span>
        </ItemTitle>
        <ItemDescription className="flex min-w-0 items-center gap-2 text-xs">
          <Code>{d.sha}</Code>
          <GitBranchIcon className="size-3 shrink-0" />
          <span className="truncate">{d.branch}</span>
        </ItemDescription>
      </ItemContent>
      <ItemActions className="shrink-0 gap-4">
        <span className="hidden w-44 lg:block">
          <LoomThread steps={d.steps} size="sm" />
        </span>
        <span
          className={cn(
            "hidden w-16 text-right text-sm tabular-nums md:block",
            d.status === "running" ? "text-foreground" : "text-muted-foreground"
          )}
        >
          {duration(d.elapsedMs)}
        </span>
        <span
          className="flex items-center justify-end gap-2 text-sm text-muted-foreground"
          title={dateTime(d.startedAt)}
        >
          <PersonAvatar id={d.author} />
          <span className="hidden w-16 text-right whitespace-nowrap sm:inline">
            {relative(d.startedAt, now)}
          </span>
          <span className="sr-only">by {person(d.author).name}</span>
        </span>
        <ChevronRightIcon className="hidden size-4 text-muted-foreground/50 transition-transform group-hover:translate-x-0.5 group-hover:text-foreground md:block" />
      </ItemActions>
    </Item>
  )
}
