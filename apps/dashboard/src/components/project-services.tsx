import {
  ChevronRightIcon,
  EyeOffIcon,
  SearchIcon,
  SlidersHorizontalIcon,
} from "lucide-react"
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
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { kindLabel, resourceLabel } from "@/lib/architecture"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import {
  covering,
  deployedConfig,
  exclusionOf,
  filterRows,
  isDeployed,
  locationOf,
  matchesPath,
  reasonLabel,
  type ServiceFilter,
  type ServiceRow,
  serviceRows,
  stateOf,
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
  const { states } = useQueries(useBindingsList, args)
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
  return { rows, bindings, routes: list }
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

const stateDot = {
  live: "bg-overdye",
  drifted: "bg-weld",
  none: "bg-warp",
} as const

/** ServiceState shows where a service stands in one environment. */
export function ServiceStateLabel({
  binding,
  className,
}: {
  binding?: Binding
  className?: string
}) {
  const state = stateOf(binding)
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-[12.5px] whitespace-nowrap",
        state === "live" && "font-medium text-overdye-ink",
        state === "drifted" && "font-medium text-weld-ink",
        state === "none" && "text-muted-foreground",
        className
      )}
    >
      <span className={cn("size-1.5 rounded-full", stateDot[state])} />
      {state === "live"
        ? "Live"
        : state === "drifted"
          ? binding?.drift === "missing"
            ? "Deleted outside Loomplane"
            : "Changed outside Loomplane"
          : "Not deployed"}
    </span>
  )
}

/**
 * ProjectServices is the Services tab: what the project deploys, where
 * each service runs, its domains and what it uses, with the services the
 * project excludes one filter away.
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
  const { rows, bindings } = useServiceRows(project, environments, view)
  // The resource of a kind that runs somewhere, the first environment
  // first, so a service's Uses can open its page.
  const resourceOf = (kind: string) => {
    for (const environment of environments) {
      const binding = (bindings[environment.name] ?? []).find(
        (b) => b.subject === "resource" && deployedConfig(b).kind === kind
      )
      if (binding) return { binding, environment }
    }
    return undefined
  }
  const excluded = view.excluded ?? []
  const [filter, setFilter] = useState<ServiceFilter>("all")
  const [search, setSearch] = useState("")
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [excluding, setExcluding] = useState<ServiceRow[] | null>(null)
  const exclusions = useExclusions(project)

  const shown = filter === "excluded" ? [] : filterRows(rows, filter, search)
  const shownExcluded =
    filter === "excluded"
      ? excluded.filter((x) => {
          const q = search.trim().toLowerCase()
          return (
            !q ||
            x.service.name.toLowerCase().includes(q) ||
            locationOf(x.service).toLowerCase().includes(q)
          )
        })
      : []
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

  return (
    <section aria-label="Services" className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <div
          role="radiogroup"
          aria-label="Show services"
          className="flex flex-wrap gap-0.5 rounded-[10px] bg-muted p-[3px]"
        >
          {filters.map((f) => (
            <button
              key={f.id}
              type="button"
              role="radio"
              aria-checked={filter === f.id}
              onClick={() => pick(f.id)}
              className={cn(
                "flex h-7 items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium transition-colors",
                filter === f.id
                  ? "bg-card text-foreground shadow-xs"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {f.label}
              <span
                className={cn(
                  "font-mono text-[11px]",
                  filter === f.id ? "text-primary" : "text-muted-foreground"
                )}
              >
                {f.count}
              </span>
            </button>
          ))}
        </div>
        <InputGroup className="h-8 w-full sm:w-60">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            type="search"
            aria-label="Filter services"
            placeholder="Filter by name or path"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </InputGroup>
      </div>

      {mayEdit && picked.length > 0 && (
        <div className="flex flex-wrap items-center gap-2.5 rounded-xl bg-primary/5 py-2 pr-2.5 pl-3.5 ring-1 ring-primary/25">
          <span className="flex-1 text-[13px] font-medium">
            {picked.length === 1
              ? "1 service selected"
              : `${picked.length} services selected`}
          </span>
          {filter === "excluded" ? (
            <Button
              size="sm"
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
              onClick={() =>
                setExcluding(rows.filter((r) => picked.includes(r.service.id)))
              }
            >
              Exclude from project
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setSelected(new Set())}
          >
            Clear
          </Button>
        </div>
      )}

      {filter !== "excluded" && (
        <div className="overflow-x-auto rounded-xl bg-card ring-1 ring-border">
          <table className="w-full min-w-[920px] text-[13px]">
            <thead>
              <tr className="border-b bg-muted/40 text-left text-[11.5px] font-medium tracking-wide text-muted-foreground uppercase">
                {mayEdit && (
                  <th className="w-10 py-2 pl-4">
                    <Checkbox
                      aria-label="Select every service shown"
                      checked={allPicked}
                      onCheckedChange={() =>
                        setSelected(allPicked ? new Set() : new Set(ids))
                      }
                    />
                  </th>
                )}
                <th className={cn("py-2 pr-3 font-medium", !mayEdit && "pl-4")}>
                  Service
                </th>
                <th className="py-2 pr-3 font-medium">Kind</th>
                {environments.map((e) => (
                  <th key={e.id} className="py-2 pr-3 font-medium normal-case">
                    {e.name}
                  </th>
                ))}
                <th className="py-2 pr-3 font-medium">Domains</th>
                <th className="py-2 pr-3 font-medium">Uses</th>
                <th className="w-8" />
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <tr
                  key={r.service.id}
                  className={cn(
                    "border-b transition-colors last:border-b-0 hover:bg-muted/30",
                    selected.has(r.service.id) && "bg-primary/5"
                  )}
                >
                  {mayEdit && (
                    <td className="py-2.5 pl-4">
                      <Checkbox
                        aria-label={`Select ${r.service.name}`}
                        checked={selected.has(r.service.id)}
                        onCheckedChange={() => toggle(r.service.id)}
                      />
                    </td>
                  )}
                  <td
                    className={cn(
                      "max-w-[280px] py-2.5 pr-3",
                      !mayEdit && "pl-4"
                    )}
                  >
                    <ServiceLink project={project} service={r.service} />
                    <code className="block truncate font-mono text-[11.5px] text-muted-foreground">
                      {locationOf(r.service)}
                    </code>
                  </td>
                  <td
                    className={cn(
                      "py-2.5 pr-3 text-[12.5px]",
                      !r.service.deployable && "text-muted-foreground"
                    )}
                  >
                    {kindLabel(r.service.kind)}
                  </td>
                  {environments.map((e) => (
                    <td key={e.id} className="py-2.5 pr-3">
                      <ServiceStateLabel binding={r.deployed[e.name]} />
                      {r.deployed[e.name] && (
                        <span className="block text-[11.5px] whitespace-nowrap text-muted-foreground">
                          {[
                            r.deployed[e.name]?.commitSha.slice(0, 7),
                            formatDate(r.deployed[e.name]?.updatedAt ?? ""),
                          ]
                            .filter(Boolean)
                            .join(" · ")}
                        </span>
                      )}
                    </td>
                  ))}
                  <td className="max-w-[240px] py-2.5 pr-3">
                    {r.domains.length > 0 ? (
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span className="truncate font-mono text-xs">
                          {r.domains[0]}
                        </span>
                        {r.domains.length > 1 && (
                          <span
                            className="shrink-0 rounded-full bg-muted px-1.5 font-mono text-[11px] text-muted-foreground"
                            title={r.domains.slice(1).join(", ")}
                          >
                            +{r.domains.length - 1}
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className="text-[12.5px] text-muted-foreground">
                        None
                      </span>
                    )}
                  </td>
                  <td className="py-2.5 pr-3">
                    {r.uses.length > 0 ? (
                      <span className="flex flex-wrap gap-1">
                        {r.uses.map((u) => {
                          const at = resourceOf(u)
                          const badge = (
                            <Badge
                              variant="outline"
                              className={cn(
                                "rounded-md font-normal",
                                at && "hover:bg-muted"
                              )}
                            >
                              {at ? at.binding.name : resourceLabel(u)}
                            </Badge>
                          )
                          return at ? (
                            <Link
                              key={u}
                              to="/projects/$id/catalog/$resource"
                              params={{
                                id: project.id,
                                resource: at.binding.name,
                              }}
                              search={{ environment: at.environment.id }}
                              title={`${resourceLabel(u)} in ${at.environment.name}`}
                            >
                              {badge}
                            </Link>
                          ) : (
                            <span key={u}>{badge}</span>
                          )
                        })}
                      </span>
                    ) : (
                      <span className="text-[12.5px] text-muted-foreground">
                        Nothing
                      </span>
                    )}
                  </td>
                  <td className="pr-3">
                    <Link
                      to="/projects/$id/services/$service"
                      params={{ id: project.id, service: r.service.name }}
                      aria-label={`Open ${r.service.name}`}
                      className="flex text-muted-foreground hover:text-foreground"
                    >
                      <ChevronRightIcon className="size-4" />
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {shown.length === 0 && (
            <p className="px-4 py-4 text-[13px] text-muted-foreground">
              {rows.length === 0
                ? "No services found. The analysis found nothing it recognizes as something that runs."
                : "No services match this filter."}
            </p>
          )}
        </div>
      )}

      {filter !== "excluded" && excluded.length > 0 && (
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 rounded-xl border border-dashed px-3.5 py-2.5 text-[12.5px] text-muted-foreground">
          <EyeOffIcon className="size-4" />
          <span className="flex-1">
            {excluded.length === 1
              ? "1 excluded service is hidden here and in the graph."
              : `${excluded.length} excluded services are hidden here and in the graph.`}
          </span>
          <Button size="sm" variant="outline" onClick={() => pick("excluded")}>
            Show excluded
          </Button>
        </div>
      )}

      {filter === "excluded" && (
        <div className="grid gap-2.5">
          <div className="flex flex-wrap items-start gap-x-4 gap-y-2.5">
            <p className="max-w-[760px] flex-1 text-[13px] leading-relaxed text-muted-foreground">
              Excluded services stay out of the graph, the plans and every
              deploy. Each analysis still finds them and keeps them excluded, on
              every branch, so they stop coming back. A rule excludes everything
              under a path, including services added later.
            </p>
            <span className="inline-flex items-center gap-1.5 rounded-lg bg-muted px-2.5 py-1 text-xs text-muted-foreground">
              <SlidersHorizontalIcon className="size-3.5" />
              Saved in project settings. Nothing changes in the repository.
            </span>
          </div>
          <div className="overflow-x-auto rounded-xl bg-card ring-1 ring-border">
            <table className="w-full min-w-[820px] text-[13px]">
              <thead>
                <tr className="border-b bg-muted/40 text-left text-[11.5px] font-medium tracking-wide text-muted-foreground uppercase">
                  {mayEdit && <th className="w-10 py-2 pl-4" />}
                  <th
                    className={cn("py-2 pr-3 font-medium", !mayEdit && "pl-4")}
                  >
                    Service
                  </th>
                  <th className="py-2 pr-3 font-medium">Reason</th>
                  <th className="py-2 pr-3 font-medium">Rule</th>
                  <th className="py-2 pr-3 font-medium">Excluded</th>
                  {mayEdit && <th className="w-32" />}
                </tr>
              </thead>
              <tbody>
                {shownExcluded.map((x) => {
                  const rule = exclusionOf(x, project.exclusions ?? [])
                  return (
                    <tr key={x.service.id} className="border-b last:border-b-0">
                      {mayEdit && (
                        <td className="py-2.5 pl-4">
                          <Checkbox
                            aria-label={`Select ${x.service.name}`}
                            checked={selected.has(x.service.id)}
                            onCheckedChange={() => toggle(x.service.id)}
                          />
                        </td>
                      )}
                      <td
                        className={cn(
                          "max-w-[300px] py-2.5 pr-3",
                          !mayEdit && "pl-4"
                        )}
                      >
                        <span className="block font-medium text-muted-foreground">
                          {x.service.name}
                        </span>
                        <code className="block truncate font-mono text-[11.5px] text-muted-foreground">
                          {locationOf(x.service)}
                        </code>
                      </td>
                      <td className="py-2.5 pr-3">
                        <Badge variant="secondary" className="font-normal">
                          {reasonLabel(rule?.reason ?? "other")}
                        </Badge>
                      </td>
                      <td className="py-2.5 pr-3">
                        {x.rule.kind === "path" ? (
                          <code className="font-mono text-xs">
                            {x.rule.match}
                          </code>
                        ) : (
                          <span className="text-[12.5px] text-muted-foreground">
                            Only this service
                          </span>
                        )}
                      </td>
                      <td className="py-2.5 pr-3 text-[12.5px] text-muted-foreground">
                        {rule ? formatDate(rule.createdAt) : ""}
                        {rule?.keepRunning && " · left running"}
                      </td>
                      {mayEdit && (
                        <td className="py-2.5 pr-4 text-right">
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={exclusions.pending}
                            onClick={() => includeAgain([x])}
                          >
                            Include again
                          </Button>
                        </td>
                      )}
                    </tr>
                  )
                })}
              </tbody>
            </table>
            {shownExcluded.length === 0 && (
              <p className="px-4 py-4 text-[13px] text-muted-foreground">
                {excluded.length === 0
                  ? "Nothing is excluded. Every service the analysis finds shows up in the graph and the plans."
                  : "No excluded services match this filter."}
              </p>
            )}
          </div>
        </div>
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
      className="font-medium text-foreground underline-offset-4 hover:underline"
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
