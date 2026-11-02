import { BoxIcon, EyeOffIcon, SlidersHorizontalIcon } from "lucide-react"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useBindingsList,
  useDomainRoutesList,
  useProjectsExclude,
  useProjectsGraph,
  useProjectsInclude,
} from "@/client/forge/loomplane/hooks"
import type {
  Binding,
  CloudinfraRoute,
  Environment,
  Excluded,
  ExcludeInputBody,
  Project,
  ProjectView,
  Service,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudTable,
  CloudTableRow,
  NameCell,
  Segments,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button, buttonVariants } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { kindLabel, shortSHA } from "@/lib/architecture"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import {
  covering,
  exclusionOf,
  filterExcluded,
  filterRows,
  isDeployed,
  locationOf,
  matchesPath,
  reasonLabel,
  type ServiceFilter,
  type ServiceRow,
  serviceRows,
  serviceStatus,
  suggestedRule,
} from "@/lib/project-services"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

type ExclusionInput = ExcludeInputBody["exclusions"][number]

/**
 * useServiceRows loads what runs in every environment and the project's
 * domain routes, and joins them with the graph's services.
 */
export function useServiceRows(
  project: Project,
  environments: Environment[],
  view: ProjectView | null
) {
  const args = useMemo(
    () =>
      environments.map((e) => ({
        path: { id: project.id },
        query: { environment: e.name },
      })),
    [environments, project.id]
  )
  const { states, refetch } = useQueries(useBindingsList, args)
  const routes = useQuery(useDomainRoutesList, {
    path: { projectId: project.id },
  })
  const bindings: Record<string, Binding[] | undefined> = {}
  environments.forEach((e, i) => {
    bindings[e.name] = states[i]?.data?.bindings
  })
  const list = (routes.data?.routes ?? []).filter(
    (r): r is CloudinfraRoute => r !== null
  )
  const rows = view?.graph
    ? serviceRows(view.graph, environments, bindings, list)
    : []
  return {
    rows,
    /** Each environment's bindings, undefined while they load. */
    bindings,
    routes: list,
    /** The first environment whose bindings couldn't be read. */
    bindingsFailed: states.find((s) => s.status === "error"),
    refetchBindings: refetch,
    /** The domain routes' read, for its loading and error states. */
    routesQuery: routes,
  }
}

/** useExclusions adds and removes exclusions, then rereads the graph. */
export function useExclusions(project: Project) {
  const exclude = useMutation(useProjectsExclude)
  const include = useMutation(useProjectsInclude)
  const invalidate = useInvalidate()
  const refresh = () => invalidate(useProjectsGraph)
  return {
    pending: exclude.isPending || include.isPending,
    exclude: async (exclusions: ExclusionInput[]) => {
      await exclude.mutateAsync({
        path: { id: project.id },
        body: { exclusions },
      })
      refresh()
    },
    /** include brings services back, removing every exclusion over them. */
    include: async (services: Service[]) => {
      const refs = covering(services, project.exclusions ?? [])
      if (refs.length === 0) return
      await include.mutateAsync({
        path: { id: project.id },
        body: { exclusions: refs },
      })
      refresh()
    },
  }
}

/** ServiceStatePill shows where a service stands in one environment. */
export function ServiceStatePill({
  binding,
  short,
}: {
  binding?: Binding
  /** Shorter words for a table column. */
  short?: boolean
}) {
  const { tone, label } = serviceStatus(binding, short)
  return <StatusPill tone={tone}>{label}</StatusPill>
}

/**
 * ProjectServices is the Services tab: what the project deploys and where
 * each service runs, with the services the project excludes one filter
 * away. A service's domains and what it uses are on its own page.
 */
export function ProjectServices({
  project,
  environments,
  view,
}: {
  project: Project
  environments: Environment[]
  view: ProjectView
}) {
  const { can } = useOrganization()
  const mayEdit = can("project:update")
  const { rows, bindings, bindingsFailed, refetchBindings } = useServiceRows(
    project,
    environments,
    view
  )
  const excluded = view.excluded ?? []
  const [filter, setFilter] = useState<ServiceFilter>("all")
  const [search, setSearch] = useState("")
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [excluding, setExcluding] = useState<ServiceRow[] | null>(null)
  const exclusions = useExclusions(project)

  const shown = filter === "excluded" ? [] : filterRows(rows, filter, search)
  const shownExcluded =
    filter === "excluded" ? filterExcluded(excluded, search) : []
  const ids =
    filter === "excluded"
      ? shownExcluded.map((x) => x.service.id)
      : shown.map((r) => r.service.id)
  const picked = ids.filter((id) => selected.has(id))
  const allPicked = ids.length > 0 && picked.length === ids.length

  const pick = (next: ServiceFilter) => {
    setFilter(next)
    setSelected(new Set())
  }
  const toggle = (id: string) =>
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })

  const includeAgain = async (items: Excluded[]) => {
    try {
      await exclusions.include(items.map((x) => x.service))
      setSelected(new Set())
      const names = items.map((x) => x.service.name)
      toast.success(
        `${listOf(names)} ${names.length === 1 ? "is" : "are"} back in the graph and the next plan.`
      )
    } catch (err) {
      toast.error(messageOf(err, "The exclusion wasn't removed."))
    }
  }

  const deployedCount = rows.filter(isDeployed).length
  const filters: { id: ServiceFilter; label: string; count: number }[] = [
    { id: "all", label: "All", count: rows.length },
    { id: "deployed", label: "Deployed", count: deployedCount },
    {
      id: "pending",
      label: "Not deployed",
      count: rows.length - deployedCount,
    },
    { id: "excluded", label: "Excluded", count: excluded.length },
  ]
  const q = search.trim()
  const clearFilter = (
    <Button size="sm" variant="outline" onClick={() => setSearch("")}>
      Clear filter
    </Button>
  )

  const check = mayEdit ? "22px " : ""
  const columns = `${check}minmax(0,1.2fr) 120px ${environments
    .map(() => "130px")
    .join(" ")} 70px`
  const excludedColumns = `${check}minmax(0,1fr) minmax(0,1fr) minmax(0,1.1fr) 150px 110px 120px`

  return (
    <section aria-label="Services" className="grid gap-2.5">
      <CloudSectionHead
        title="Services"
        description="Everything the analysis found that runs, and where it's deployed. Excluding one keeps it out of every plan."
      >
        <Segments
          label="Show services"
          value={filter}
          onChange={pick}
          items={filters}
        />
        <Input
          type="search"
          aria-label="Filter services"
          placeholder="Filter by name or path"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="h-[30px] w-full sm:w-52"
        />
      </CloudSectionHead>

      {mayEdit && picked.length > 0 && (
        <div className="flex flex-wrap items-center gap-2.5 rounded-xl bg-muted py-2 pr-2.5 pl-3.5">
          <span className="flex-1 text-[13px] font-medium">
            {picked.length === 1
              ? "1 service selected"
              : `${picked.length} services selected`}
          </span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setSelected(new Set())}
          >
            Clear
          </Button>
          {filter === "excluded" ? (
            <Button
              size="sm"
              variant="outline"
              disabled={exclusions.pending}
              onClick={() =>
                includeAgain(
                  excluded.filter((x) => picked.includes(x.service.id))
                )
              }
            >
              Include again
            </Button>
          ) : (
            <Button
              size="sm"
              variant="outline"
              className="text-destructive"
              onClick={() =>
                setExcluding(rows.filter((r) => picked.includes(r.service.id)))
              }
            >
              Exclude from project
            </Button>
          )}
        </div>
      )}

      {filter !== "excluded" && bindingsFailed && (
        <QueryError
          error={bindingsFailed.error}
          fallback="Where the services run couldn't be loaded."
          retry={refetchBindings}
        />
      )}

      {filter !== "excluded" &&
        (shown.length === 0 ? (
          rows.length === 0 ? (
            <CloudEmpty kind="list" icon={BoxIcon} title="No services yet">
              The analysis found nothing it recognizes as something that runs.
            </CloudEmpty>
          ) : q ? (
            <CloudEmpty
              kind="list"
              icon={BoxIcon}
              title={`No services match "${q}"`}
              action={clearFilter}
            >
              The filter checks names and paths.
            </CloudEmpty>
          ) : (
            <CloudEmpty
              kind="list"
              icon={BoxIcon}
              title={
                filter === "deployed"
                  ? "No deployed services yet"
                  : "Every service is deployed"
              }
              action={
                <Button size="sm" variant="outline" onClick={() => pick("all")}>
                  Show all
                </Button>
              }
            >
              {filter === "deployed"
                ? "Nothing of this project runs in any environment yet."
                : "Each service runs in at least one environment."}
            </CloudEmpty>
          )
        ) : (
          <CloudTable
            head={[
              ...(mayEdit
                ? [
                    <Checkbox
                      key="all"
                      aria-label="Select every service shown"
                      checked={allPicked}
                      onCheckedChange={() =>
                        setSelected(allPicked ? new Set() : new Set(ids))
                      }
                    />,
                  ]
                : []),
              "Service",
              "Kind",
              ...environments.map((e) => (
                <span key={e.id} className="truncate normal-case">
                  {e.name}
                </span>
              )),
              <span key="open" className="sr-only">
                Actions
              </span>,
            ]}
            label="Services"
            columns={columns}
            minWidth={560 + environments.length * 140}
            footer={
              <>
                <span>
                  {shown.length === rows.length
                    ? `Showing ${rows.length} ${rows.length === 1 ? "service" : "services"}`
                    : `${shown.length} of ${rows.length} services match`}
                </span>
                {excluded.length > 0 && (
                  <span className="flex flex-wrap items-center gap-2">
                    <EyeOffIcon className="size-3.5" aria-hidden="true" />
                    {excluded.length === 1
                      ? "1 excluded service is hidden here and in the graph."
                      : `${excluded.length} excluded services are hidden here and in the graph.`}
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => pick("excluded")}
                    >
                      Show excluded
                    </Button>
                  </span>
                )}
              </>
            }
          >
            {shown.map((r) => (
              <CloudTableRow
                key={r.service.id}
                className={cn(selected.has(r.service.id) && "bg-primary/5")}
              >
                {mayEdit && (
                  <Cell className="flex">
                    <Checkbox
                      aria-label={`Select ${r.service.name}`}
                      checked={selected.has(r.service.id)}
                      onCheckedChange={() => toggle(r.service.id)}
                    />
                  </Cell>
                )}
                <NameCell
                  name={<ServiceLink project={project} service={r.service} />}
                  meta={
                    <code className="font-mono text-[11.5px]">
                      {locationOf(r.service)}
                    </code>
                  }
                />
                <Cell
                  className={cn(
                    "text-[12.5px]",
                    !r.service.deployable && "text-muted-foreground"
                  )}
                >
                  {kindLabel(r.service.kind)}
                </Cell>
                {environments.map((e) => {
                  const b = r.deployed[e.name]
                  return (
                    <Cell key={e.id}>
                      {bindings[e.name] === undefined && !bindingsFailed ? (
                        <Skeleton className="h-5 w-20 rounded-full" />
                      ) : (
                        <span
                          title={
                            b
                              ? [shortSHA(b.commitSha), formatDate(b.updatedAt)]
                                  .filter(Boolean)
                                  .join(" · ")
                              : undefined
                          }
                        >
                          <ServiceStatePill binding={b} short />
                        </span>
                      )}
                    </Cell>
                  )
                })}
                <ActionsCell>
                  <Link
                    to="/projects/$id/services/$service"
                    params={{ id: project.id, service: r.service.name }}
                    aria-label={`Open ${r.service.name}`}
                    className={buttonVariants({
                      size: "sm",
                      variant: "outline",
                    })}
                  >
                    Open
                  </Link>
                </ActionsCell>
              </CloudTableRow>
            ))}
          </CloudTable>
        ))}

      {filter === "excluded" && (
        <>
          <CloudCallout
            tone="info"
            icon={SlidersHorizontalIcon}
            title="Saved in project settings. Nothing changes in the repository."
          >
            Excluded services stay out of the graph, the plans and every deploy.
            Each analysis still finds them and keeps them excluded, on every
            branch, so they stop coming back. A rule excludes everything under a
            path, including services added later.
          </CloudCallout>
          {shownExcluded.length === 0 ? (
            excluded.length === 0 ? (
              <CloudEmpty
                kind="list"
                icon={EyeOffIcon}
                title="No excluded services yet"
              >
                Every service the analysis finds shows up in the graph and the
                plans.
              </CloudEmpty>
            ) : (
              <CloudEmpty
                kind="list"
                icon={EyeOffIcon}
                title={`No excluded services match "${q}"`}
                action={clearFilter}
              >
                The filter checks names and paths.
              </CloudEmpty>
            )
          ) : (
            <CloudTable
              head={[
                ...(mayEdit ? [<span key="pick" />] : []),
                "Service",
                "Reason",
                "Rule",
                "If it runs",
                "Excluded",
                <span key="act" className="sr-only">
                  Actions
                </span>,
              ]}
              label="Excluded services"
              columns={excludedColumns}
              minWidth={860}
              footer={
                <span>
                  {shownExcluded.length === excluded.length
                    ? `Showing ${excluded.length} excluded ${excluded.length === 1 ? "service" : "services"}`
                    : `${shownExcluded.length} of ${excluded.length} excluded services match`}
                </span>
              }
            >
              {shownExcluded.map((x) => {
                const rule = exclusionOf(x, project.exclusions ?? [])
                return (
                  <CloudTableRow
                    key={x.service.id}
                    className={cn(selected.has(x.service.id) && "bg-primary/5")}
                  >
                    {mayEdit && (
                      <Cell className="flex">
                        <Checkbox
                          aria-label={`Select ${x.service.name}`}
                          checked={selected.has(x.service.id)}
                          onCheckedChange={() => toggle(x.service.id)}
                        />
                      </Cell>
                    )}
                    <NameCell
                      name={x.service.name}
                      meta={
                        <code className="font-mono text-[11.5px]">
                          {locationOf(x.service)}
                        </code>
                      }
                    />
                    <Cell className="text-[12.5px]">
                      {reasonLabel(rule?.reason ?? "other")}
                    </Cell>
                    <Cell>
                      {x.rule.kind === "path" ? (
                        <code className="font-mono text-xs break-all">
                          {x.rule.match}
                        </code>
                      ) : (
                        <span className="text-[12.5px] text-muted-foreground">
                          Only this service
                        </span>
                      )}
                    </Cell>
                    <Cell className="text-[12.5px] text-muted-foreground">
                      {rule?.keepRunning
                        ? "Left running"
                        : "Removed at next deploy"}
                    </Cell>
                    <Cell className="text-[12.5px] text-muted-foreground">
                      {rule ? formatDate(rule.createdAt) : ""}
                    </Cell>
                    <ActionsCell>
                      {mayEdit && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={exclusions.pending}
                          onClick={() => includeAgain([x])}
                        >
                          Include again
                        </Button>
                      )}
                    </ActionsCell>
                  </CloudTableRow>
                )
              })}
            </CloudTable>
          )}
        </>
      )}

      {excluding && (
        <ExcludeDialog
          project={project}
          rows={excluding}
          all={view.graph?.services ?? []}
          onClose={(done) => {
            setExcluding(null)
            if (done) setSelected(new Set())
          }}
        />
      )}
    </section>
  )
}

function ServiceLink({
  project,
  service,
}: {
  project: Project
  service: Service
}) {
  return (
    <Link
      to="/projects/$id/services/$service"
      params={{ id: project.id, service: service.name }}
      className="text-foreground underline-offset-2 hover:underline"
    >
      {service.name}
    </Link>
  )
}

const reasons = [
  {
    id: "tooling",
    label: "Build or CI tooling",
    hint: "Runs in a pipeline, not as a service.",
  },
  { id: "fixture", label: "Test fixture", hint: "Only there for tests." },
  {
    id: "elsewhere",
    label: "Deployed elsewhere",
    hint: "Another project or system owns it.",
  },
  { id: "other", label: "Something else", hint: "None of these." },
] as const

/**
 * ExcludeDialog confirms excluding services: why, whether a path rule
 * takes the directory they're in, and, for a service that runs somewhere,
 * whether the next plan removes it or leaves it running.
 */
export function ExcludeDialog({
  project,
  rows,
  all,
  onClose,
}: {
  project: Project
  rows: ServiceRow[]
  all: Service[]
  onClose: (done: boolean) => void
}) {
  const exclusions = useExclusions(project)
  const services = rows.map((r) => r.service)
  const rule = suggestedRule(services)
  const [reason, setReason] = useState<string>(
    rule?.startsWith("test") ? "fixture" : "tooling"
  )
  const [useRule, setUseRule] = useState(!!rule && reason === "fixture")
  const running = rows.filter(isDeployed)
  const [keepRunning, setKeepRunning] = useState(false)

  const also = rule
    ? all.filter(
        (s) =>
          !services.some((x) => x.id === s.id) &&
          (matchesPath(rule, s.path) || matchesPath(rule, s.entrypoint))
      )
    : []
  const count = services.length + (useRule ? also.length : 0)
  const title =
    services.length === 1
      ? `Exclude ${services[0].name} from this project?`
      : `Exclude ${services.length} services from this project?`

  const submit = async () => {
    const list: ExclusionInput[] =
      useRule && rule
        ? [
            { kind: "path", match: rule, reason },
            ...services
              .filter(
                (s) =>
                  !matchesPath(rule, s.path) && !matchesPath(rule, s.entrypoint)
              )
              .map((s) => ({
                kind: "service",
                match: s.id,
                name: s.name,
                reason,
                keepRunning,
              })),
          ]
        : services.map((s) => ({
            kind: "service",
            match: s.id,
            name: s.name,
            reason,
            keepRunning,
          }))
    try {
      await exclusions.exclude(list)
      toast.success(
        `${count === 1 ? `${services[0].name} is` : `${count} services are`} excluded. They leave the graph and the next plan.`
      )
      onClose(true)
    } catch (err) {
      toast.error(messageOf(err, "The services weren't excluded."))
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose(false)}>
      <DialogContent className="sm:max-w-[540px]">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            They leave the graph, the services list and every new plan, on every
            branch and in every environment. Analyses still find them and keep
            them excluded. This is saved in the project&apos;s settings, so
            nothing changes in the repository.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <span id="exclude-why" className="text-[12.5px] font-medium">
              Why
            </span>
            <div
              role="radiogroup"
              aria-labelledby="exclude-why"
              className="grid gap-2 sm:grid-cols-2"
            >
              {reasons.map((r) => (
                <button
                  key={r.id}
                  type="button"
                  role="radio"
                  aria-checked={reason === r.id}
                  onClick={() => setReason(r.id)}
                  className={cn(
                    "grid gap-0.5 rounded-lg bg-card px-3 py-2.5 text-left ring-1 transition-shadow",
                    reason === r.id
                      ? "ring-2 ring-primary"
                      : "ring-border hover:ring-foreground/25"
                  )}
                >
                  <span className="text-[13px] font-semibold">{r.label}</span>
                  <span className="text-xs text-muted-foreground">
                    {r.hint}
                  </span>
                </button>
              ))}
            </div>
          </div>

          {rule && (
            <label className="flex cursor-pointer items-start gap-2.5 rounded-lg px-3 py-2.5 ring-1 ring-border">
              <Checkbox
                className="mt-0.5"
                checked={useRule}
                onCheckedChange={(v) => setUseRule(!!v)}
              />
              <span className="grid gap-0.5">
                <span className="text-[13px] font-medium">
                  Exclude everything under{" "}
                  <code className="font-mono text-[12.5px]">{rule}</code>
                </span>
                <span className="text-xs text-muted-foreground">
                  {also.length > 0
                    ? `Also excludes ${listOf(also.map((s) => s.name))}, and anything added there later.`
                    : "Anything added there later is excluded too."}
                </span>
              </span>
            </label>
          )}

          {running.length > 0 && (
            <div className="grid gap-2 rounded-lg bg-weld-tint p-3 ring-1 ring-weld/30">
              <span className="text-[13px] font-medium text-weld-ink">
                {running.length === 1
                  ? `${running[0].service.name} is running`
                  : `${listOf(running.map((r) => r.service.name))} are running`}
              </span>
              <div
                role="radiogroup"
                aria-label="What happens to what's running"
                className="grid gap-1.5"
              >
                {[
                  {
                    keep: false,
                    label: "Remove it at the next deploy",
                    hint: "The plan shows it as a delete, so it still goes through approval.",
                  },
                  {
                    keep: true,
                    label: "Leave it running as it is",
                    hint: "Plans keep what's deployed and never change it.",
                  },
                ].map((o) => (
                  <button
                    key={String(o.keep)}
                    type="button"
                    role="radio"
                    aria-checked={keepRunning === o.keep}
                    onClick={() => setKeepRunning(o.keep)}
                    className={cn(
                      "flex items-start gap-2.5 rounded-lg px-2.5 py-2 text-left ring-1",
                      keepRunning === o.keep
                        ? "bg-card ring-weld/40"
                        : "ring-transparent"
                    )}
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        "mt-0.5 flex size-3.5 shrink-0 items-center justify-center rounded-full ring-1",
                        keepRunning === o.keep ? "ring-primary" : "ring-border"
                      )}
                    >
                      {keepRunning === o.keep && (
                        <span className="size-1.5 rounded-full bg-primary" />
                      )}
                    </span>
                    <span className="grid gap-0.5">
                      <span className="text-[13px] font-medium">{o.label}</span>
                      <span className="text-xs text-muted-foreground">
                        {o.hint}
                      </span>
                    </span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onClose(false)}>
            Cancel
          </Button>
          <Button disabled={exclusions.pending} onClick={submit}>
            {count === 1
              ? `Exclude ${services[0].name}`
              : `Exclude ${count} services`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * ExcludedServices lists what the graph leaves out, under the graph, each
 * with a way back in.
 */
export function ExcludedServices({
  project,
  excluded,
}: {
  project: Project
  excluded: Excluded[]
}) {
  const { can } = useOrganization()
  const exclusions = useExclusions(project)
  if (excluded.length === 0) return null
  return (
    <details className="group rounded-xl border border-dashed px-3.5 py-2.5">
      <summary className="cursor-pointer text-[12.5px] text-muted-foreground">
        {excluded.length === 1
          ? "1 excluded service isn't drawn"
          : `${excluded.length} excluded services aren't drawn`}
      </summary>
      <div className="mt-2.5 flex flex-wrap gap-2">
        {excluded.map((x) => {
          const rule = exclusionOf(x, project.exclusions ?? [])
          return (
            <span
              key={x.service.id}
              className="inline-flex items-center gap-2.5 rounded-lg bg-muted py-1.5 pr-1.5 pl-3 text-[12.5px] text-muted-foreground"
            >
              <span className="grid">
                <span className="font-medium text-foreground/80">
                  {x.service.name}
                </span>
                <span className="text-[11.5px]">
                  {reasonLabel(rule?.reason ?? "other")}
                </span>
              </span>
              {can("project:update") && (
                <Button
                  size="xs"
                  variant="outline"
                  disabled={exclusions.pending}
                  onClick={() =>
                    exclusions
                      .include([x.service])
                      .catch((err: unknown) =>
                        toast.error(
                          messageOf(err, "The exclusion wasn't removed.")
                        )
                      )
                  }
                >
                  Include
                </Button>
              )}
            </span>
          )
        })}
      </div>
    </details>
  )
}

function listOf(names: string[]): string {
  if (names.length <= 1) return names.join("")
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
}
