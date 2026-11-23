import { BoxIcon, RocketIcon } from "lucide-react"
import { type ReactNode, useMemo, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import { useBindingsList } from "@/client/forge/loomplane/hooks"
import type {
  Analysis,
  Binding,
  Environment,
  Graph,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import { useQueries } from "@/client/queries"
import {
  AnalysisBanner,
  AnalysisHistory,
  AnalyzeActions,
  ConnectRepository,
  GraphCaption,
  NotAnalyzed,
  ResourceDetail,
  ServiceDetail,
  ServicesTable,
  Structure,
  type useAnalyses,
} from "@/components/architecture"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { DatabaseGateways } from "@/components/database-gateway"
import {
  DeployTab,
  ManifestSettings,
  WhereServicesRun,
} from "@/components/deploy"
import { AddEnvironment, Environments } from "@/components/environment-manager"
import { ProductIcon } from "@/components/product-icon"
import { DangerZone } from "@/components/project-delete"
import { projectCrumbs, useProjectFrame } from "@/components/project-frame"
import { ProjectGeneral } from "@/components/project-general"
import {
  type AttentionItem,
  NeedsAttention,
  type SetupStep,
  SetupSteps,
  WhatRunsWhere,
} from "@/components/project-overview"
import { ProjectServices, useServiceRows } from "@/components/project-services"
import { ProjectAutonomy } from "@/components/project-autonomy"
import { ReleaseReportsCard } from "@/components/release-reports-card"
import { ProjectTeam } from "@/components/project-team"
import { ProjectVariables } from "@/components/project-variables"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import {
  type GraphSelection,
  needsReview,
  ServiceGraph,
} from "@/components/service-graph"
import { SiteHeader } from "@/components/site-header"
import { Repositories } from "@/components/sources"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { AddServiceButton, useFoundServices } from "@/lib/add-service-context"
import { isActive, resourceLabel, shortSHA } from "@/lib/architecture"
import {
  catalogKind,
  catalogResources,
  parseResourceConfig,
} from "@/lib/catalog-kinds"
import { formatDate, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useProjectCan } from "@/lib/project-permissions"
import {
  type ProjectTab,
  type SettingsSection,
  settingsSections,
} from "@/lib/project-nav"
import { isReady } from "@/lib/projects"
import { Link, useNavigate } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"
import { CloudsWorkspace } from "@/pages/clouds"

export type { ProjectTab } from "@/lib/project-nav"

const tabLabels: Record<ProjectTab, string> = {
  overview: "Overview",
  services: "Services",
  deployments: "Deployments",
  variables: "Variables",
  settings: "Settings",
}

// While the environments load: one array, so the queries keyed on it settle.
const noEnvironments: Environment[] = []

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

/**
 * ProjectViews is the project's own page inside its frame: one of its
 * views, chosen by ?tab=. The frame above it holds the project's header
 * and the nav between the views.
 */
export function ProjectViews({
  tab = "overview",
  section,
}: {
  tab?: ProjectTab
  /** The part of Settings to open on, from ?section=. */
  section?: SettingsSection
}) {
  const { project, view } = useProjectFrame()
  const [selected, setSelected] = useState<GraphSelection | null>(null)
  const graph = view?.graph ?? null
  const selectedService =
    selected?.kind === "service"
      ? graph?.services.find((s) => s.id === selected.id)
      : undefined
  const selectedResource =
    selected?.kind === "resource"
      ? graph?.resources.find((r) => r.id === selected.id)
      : undefined

  return (
    <>
      <SiteHeader
        crumbs={
          tab === "overview"
            ? [{ label: "Projects", to: "/projects" }, { label: project.name }]
            : projectCrumbs(project, { label: tabLabels[tab] })
        }
      />
      <div className="grid gap-6">
        {tab === "overview" && <Overview onSelect={setSelected} />}
        {tab === "services" && <Services onSelect={setSelected} />}
        {tab === "deployments" && (
          <div className="grid gap-6">
            <ReleaseReportsCard projectId={project.id} />
            <DeployTab key={project.id} project={project} />
          </div>
        )}
        {tab === "variables" && <Variables />}
        {tab === "settings" && <Settings section={section} />}
      </div>

      <Sheet
        open={!!selectedService || !!selectedResource}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
      >
        <SheetContent className="w-full gap-0 sm:max-w-md">
          {graph && selectedService && (
            <ServiceDetail
              sources={graph.sources ?? []}
              graph={graph}
              service={selectedService}
              onSelect={setSelected}
            />
          )}
          {graph && selectedResource && (
            <ResourceDetail
              sources={graph.sources ?? []}
              graph={graph}
              resource={selectedResource}
              onSelect={setSelected}
            />
          )}
        </SheetContent>
      </Sheet>
    </>
  )
}

/**
 * Overview is the project in the order you'd set it up: the steps left
 * before it runs, what waits on someone, what runs in each environment,
 * the environments themselves, and how the code fits together.
 */
function Overview({ onSelect }: { onSelect: (s: GraphSelection) => void }) {
  const { project, environments, analyses, view, viewError, runs } =
    useProjectFrame()
  const can = useProjectCan(project.id)
  const navigate = useNavigate()
  const mayClouds = useOrganization().can("cloud_account:read")
  const now = useNow(60_000)
  const list = environments.list
  const graph = view?.graph ?? null
  const hasRepositories = project.repositories.length > 0
  // A service project from the catalog has no code: what runs is its
  // resources, and there's no graph to draw.
  const fromCatalog = project.kind === "service" && !hasRepositories
  const fleet = project.deploymentMode === "fleet"
  const toReview = graph?.resources.filter(needsReview) ?? []
  const found = useFoundServices(project.id)
  const shownAnalyses = (view?.sources ?? []).flatMap((s) =>
    s.shown ? [s.shown] : []
  )
  const shown = shownAnalyses.length === 1 ? shownAnalyses[0] : undefined
  const { bindings } = useServiceRows(project, list ?? noEnvironments, view)
  const answered = !!list && list.every((e) => bindings[e.name] !== undefined)
  const deployed = Object.values(bindings).some((b) =>
    b?.some((x) => x.subject === "service")
  )

  // The steps show until something is deployed, once it's known whether
  // anything is.
  const settingUp =
    !fromCatalog && !fleet && answered && !deployed && analyses.loaded
  const steps = setupSteps({
    project,
    environments: list ?? [],
    analyses,
    graph,
    toReview: toReview.length,
    found,
    may: {
      run: can("analysis:run"),
      environments: can("environment:manage"),
      deploy: can("deployment:create") && isReady(project.status),
    },
    review: () =>
      toReview[0] && onSelect({ kind: "resource", id: toReview[0].id }),
  })

  const attention: AttentionItem[] = []
  const latest = analyses.latest
  if (latest?.status === "failed")
    attention.push({
      id: "analysis",
      tone: "failed",
      title: `The latest analysis${latest.commitSha ? ` of ${shortSHA(latest.commitSha)}` : ""} failed`,
      detail: latest.error,
      action: (
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={
            <Link
              to="/projects/$id"
              params={{ id: project.id }}
              search={{ tab: "settings", section: "source" }}
            />
          }
        >
          Analyses
        </Button>
      ),
    })
  // While the steps show, they already ask for these.
  if (!settingUp) {
    for (const r of toReview)
      attention.push({
        id: `review:${r.id}`,
        tone: "waiting",
        title: `Loomplane thinks ${project.name} needs ${needName(r.kind)}, but isn't sure`,
        detail: `${
          r.evidence[0]
            ? `Read from ${r.evidence[0].path}${r.evidence[0].line ? `:${r.evidence[0].line}` : ""}. `
            : ""
        }Nothing is provisioned for it until someone confirms it.`,
        action: (
          <Button
            size="sm"
            variant="outline"
            onClick={() => onSelect({ kind: "resource", id: r.id })}
          >
            Review
          </Button>
        ),
      })
    if (found)
      attention.push({
        id: "found",
        tone: "waiting",
        title: found.title,
        detail: found.detail,
        action: (
          <Button size="sm" variant="outline" onClick={found.open}>
            Review and add
          </Button>
        ),
      })
  }
  const waiting = runs.filter((r) => r.status === "waiting")
  if (waiting.length > 0)
    attention.push({
      id: "waiting",
      tone: "waiting",
      title: `${plural(waiting.length, "plan")} ${waiting.length === 1 ? "waits" : "wait"} for approval`,
      detail: `In ${[...new Set(waiting.map((r) => r.environment))].join(", ")}. Nothing changes until someone approves.`,
      action: (
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={
            waiting.length === 1 ? (
              <Link
                to="/projects/$id/deployments/$runId"
                params={{ id: project.id, runId: waiting[0].id }}
              />
            ) : (
              <Link
                to="/projects/$id"
                params={{ id: project.id }}
                search={{ tab: "deployments" }}
              />
            )
          }
        >
          Review
        </Button>
      ),
    })

  return (
    <>
      {latest && isActive(latest.status) && (
        <AnalysisBanner analysis={latest} />
      )}
      {settingUp && <SetupSteps steps={steps} />}
      <NeedsAttention items={attention} />
      {list && (
        <WhatRunsWhere project={project} environments={list} view={view} />
      )}
      <Environments
        project={project}
        environments={list}
        error={environments.error}
        retry={environments.refetch}
        mayManage={can("environment:manage")}
        mayDeploy={can("deployment:create")}
        mayClouds={mayClouds}
        runs={runs}
        onOpen={(next, env) =>
          void navigate({
            to: "/projects/$id",
            params: { id: project.id },
            search:
              next === "cloud"
                ? { tab: "settings", section: "clouds" }
                : { tab: next, environment: env },
          })
        }
      />
      {!fromCatalog && (
        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_300px]">
          <section aria-label="Service graph" className="grid gap-2.5">
            <CloudSectionHead
              title="How it fits together"
              description={graphNote(shown, view?.excluded.length ?? 0, now)}
            />
            <Discovered
              project={project}
              analyses={analyses}
              view={view}
              viewError={viewError}
              mayRun={can("analysis:run")}
              mayEdit={can("project:update")}
            >
              {(g) =>
                g.services.length + g.resources.length > 0 ? (
                  <>
                    <ServiceGraph
                      graph={g}
                      selected={null}
                      onSelect={onSelect}
                    />
                    <GraphCaption graph={g} />
                  </>
                ) : (
                  <ServicesTable graph={g} onSelect={onSelect} />
                )
              }
            </Discovered>
          </section>
          <About
            project={project}
            graph={graph}
            shown={shown}
            excluded={view?.excluded.length ?? 0}
          />
        </div>
      )}
    </>
  )
}

/**
 * setupSteps is the way from a new project to its first deploy: code,
 * what runs in it, what it needs, somewhere to run, then running it.
 */
function setupSteps({
  project,
  environments,
  analyses,
  graph,
  toReview,
  found,
  may,
  review,
}: {
  project: Project
  environments: Environment[]
  analyses: ReturnType<typeof useAnalyses>
  graph: Graph | null
  toReview: number
  found: ReturnType<typeof useFoundServices>
  may: { run: boolean; environments: boolean; deploy: boolean }
  review: () => void
}): SetupStep[] {
  const services = graph?.services.length ?? 0
  const analyzed = !!analyses.shown
  return [
    {
      id: "repository",
      title: "Connect a repository",
      done: project.repositories.length > 0,
      detail:
        "Loomplane reads the code to find what runs and what it needs. Add the repository below.",
    },
    {
      id: "analysis",
      title: "Find what runs",
      done: services > 0,
      busy: analyses.running,
      detail: analyses.running
        ? "Loomplane is reading the repository now. Each service it finds shows up here."
        : analyzed
          ? "The last analysis found nothing that runs. Try another branch, or check the repository."
          : "An analysis reads a branch and finds each service and what it needs.",
      action: may.run && !analyses.running && (
        <AnalyzeActions
          project={project}
          running={analyses.running}
          size="sm"
        />
      ),
    },
    {
      id: "review",
      title: "Confirm what it needs",
      // Nothing can be confirmed before anything is found.
      done: services > 0 && toReview === 0 && !found,
      detail: found
        ? found.title
        : "Loomplane wasn't sure about some of what the services need. Nothing is provisioned until someone confirms it.",
      action: found ? (
        <Button size="sm" variant="outline" onClick={found.open}>
          Review and add
        </Button>
      ) : (
        <Button size="sm" variant="outline" onClick={review}>
          Review
        </Button>
      ),
    },
    {
      id: "environment",
      title: "Add an environment",
      done: environments.length > 0,
      detail:
        "Such as production or staging. Each has its own variables and the cloud it runs on.",
      action: may.environments && <AddEnvironment project={project} />,
    },
    {
      id: "deploy",
      title: "Deploy it",
      done: false,
      detail:
        "Loomplane plans what to create in the environment and waits for your approval before it changes anything.",
      action: may.deploy && (
        <Button
          size="sm"
          nativeButton={false}
          render={
            <Link
              to="/deployments/new"
              search={{ project: project.id, environment: undefined }}
            />
          }
        >
          <RocketIcon />
          Deploy
        </Button>
      ),
    },
  ]
}

/**
 * Services is what the project runs: its own services first, then the
 * databases and caches they use, each opening on its own page.
 */
function Services({ onSelect }: { onSelect: (s: GraphSelection) => void }) {
  const { project, environments, analyses, view, viewError } = useProjectFrame()
  const can = useProjectCan(project.id)
  const mayEdit = can("project:update")
  const fromCatalog =
    project.kind === "service" && project.repositories.length === 0
  return (
    <>
      {mayEdit && project.deploymentMode !== "fleet" && (
        <div className="-mb-3 flex justify-end">
          <AddServiceButton size="sm" />
        </div>
      )}
      {fromCatalog ? (
        <WhereServicesRun project={project} />
      ) : (
        <Discovered
          project={project}
          analyses={analyses}
          view={view}
          viewError={viewError}
          mayRun={can("analysis:run")}
          mayEdit={mayEdit}
        >
          {(g) =>
            view ? (
              <ProjectServices
                project={project}
                environments={environments.list ?? []}
                view={view}
              />
            ) : (
              <ServicesTable graph={g} onSelect={onSelect} />
            )
          }
        </Discovered>
      )}
      <Resources project={project} environments={environments.list} />
      <DatabaseGateways project={project.id} />
    </>
  )
}

/** Variables is the project's variables in the environment the nav shows. */
function Variables() {
  const { project, environments, environment } = useProjectFrame()
  const can = useProjectCan(project.id)
  if (environments.error !== undefined && !environments.list)
    return (
      <QueryError
        error={environments.error}
        fallback="The environments couldn't be loaded."
        retry={environments.refetch}
      />
    )
  if (!environments.list) return <Skeleton className="h-32 rounded-xl" />
  if (environments.list.length === 0 || !environment)
    return (
      <CloudEmpty
        title="No environments yet"
        action={
          can("environment:manage") && <AddEnvironment project={project} />
        }
      >
        Variables are set per environment. Add one, such as production, to set
        them.
      </CloudEmpty>
    )
  return (
    <ProjectVariables
      project={project}
      environments={environments.list}
      environment={environment.name}
      chosen
    />
  )
}

const sectionLabels: Record<SettingsSection, string> = {
  general: "General",
  access: "Team and autonomy",
  source: "Source",
  clouds: "Clouds",
  manifest: "loomplane.yaml",
}

/**
 * Settings is how the project is set up rather than what it runs, one
 * part at a time: its name and the danger zone, who owns it, the code it
 * reads, the clouds it may use, and its manifest.
 */
function Settings({ section: wanted }: { section?: SettingsSection }) {
  const { project, environments, analyses, view, requestDelete } =
    useProjectFrame()
  const can = useProjectCan(project.id)
  const mayClouds = useOrganization().can("cloud_account:read")
  const sections = settingsSections.filter((s) => s !== "clouds" || mayClouds)
  const section = wanted && sections.includes(wanted) ? wanted : "general"
  const hasRepositories = project.repositories.length > 0
  const graph = view?.graph ?? null
  const copyID = () => {
    void navigator.clipboard?.writeText(project.id)
    toast.success("Copied the project ID")
  }

  return (
    <div className="grid gap-6 md:grid-cols-[180px_minmax(0,1fr)] md:gap-8">
      <nav aria-label="Settings" className="md:sticky md:top-20 md:self-start">
        <ul className="flex gap-1 overflow-x-auto md:grid md:gap-0.5">
          {sections.map((s) => (
            <li key={s}>
              <Link
                to="/projects/$id"
                params={{ id: project.id }}
                search={{
                  tab: "settings",
                  ...(s === "general" ? {} : { section: s }),
                }}
                aria-current={s === section ? "page" : undefined}
                className={cn(
                  "flex h-8 items-center rounded-md px-2.5 text-[13px] whitespace-nowrap transition-colors",
                  s === section
                    ? "bg-muted font-medium text-foreground"
                    : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                )}
              >
                {sectionLabels[s]}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <div
        className={cn(
          "grid min-w-0 content-start gap-6",
          section !== "clouds" && "max-w-[760px]"
        )}
      >
        {section === "general" && (
          <>
            <ProjectGeneral
              key={`${project.id}:${project.updatedAt}`}
              project={project}
              mayEdit={can("project:update")}
            />
            <Details project={project} onCopy={copyID} />
            {can("project:delete") && (
              <DangerZone
                project={project}
                environments={environments.list}
                onDelete={requestDelete}
              />
            )}
          </>
        )}
        {section === "access" && (
          <>
            <ProjectTeam project={project} />
            <ProjectAutonomy project={project} />
          </>
        )}
        {section === "source" && (
          <>
            {hasRepositories ? (
              <Repositories project={project} />
            ) : (
              <ConnectRepository
                project={project}
                mayEdit={can("project:update")}
              />
            )}
            <section aria-label="Analyses" className="grid gap-2.5">
              <CloudSectionHead
                title="Analyses"
                count={analyses.loaded ? analyses.list.length : undefined}
                description="Each time Loomplane read a branch to find what runs. The newest one draws the graph."
              >
                {hasRepositories && can("analysis:run") && (
                  <AnalyzeActions
                    project={project}
                    running={analyses.running}
                    size="sm"
                  />
                )}
              </CloudSectionHead>
              {analyses.error ? (
                <CloudCallout tone="error" title="Analyses couldn't be loaded">
                  {messageOf(analyses.error, "Try again in a moment.")}
                </CloudCallout>
              ) : !analyses.loaded ? (
                <Skeleton className="h-32 rounded-xl" />
              ) : (
                <AnalysisHistory analyses={analyses.list} />
              )}
            </section>
            {graph && view?.sources.some((s) => s.shown) && (
              <Structure sources={graph.sources ?? []} graph={graph} />
            )}
          </>
        )}
        {section === "clouds" && <CloudsWorkspace projectId={project.id} />}
        {section === "manifest" && <ManifestSettings project={project} />}
      </div>
    </div>
  )
}

/** graphNote says where the graph comes from and what it leaves out. */
function graphNote(
  shown: Analysis | undefined,
  excluded: number,
  now: number
): string {
  const from = shown
    ? `From the last analysis, ${relative(Date.parse(shown.finishedAt ?? shown.createdAt), now)}.`
    : "What the analyses found, and how it connects."
  if (excluded === 0) return from
  return `${from} ${plural(excluded, "excluded service")} ${excluded === 1 ? "isn't" : "aren't"} drawn.`
}

/** About is what Loomplane knows about the project, beside its graph. */
function About({
  project,
  graph,
  shown,
  excluded,
}: {
  project: Project
  graph: Graph | null
  shown?: Analysis
  excluded: number
}) {
  const now = useNow(60_000)
  const services = graph?.services.length ?? 0
  const facts: { label: string; value: ReactNode }[] = [
    {
      label: "Repositories",
      value: project.repositories.length || "None yet",
    },
  ]
  if (shown)
    facts.push(
      { label: "Branch", value: shown.ref || "Default branch" },
      {
        label: "Last analyzed",
        value: relative(Date.parse(shown.finishedAt ?? shown.createdAt), now),
      }
    )
  if (graph)
    facts.push({
      label: "Services",
      value: excluded ? `${services}, plus ${excluded} excluded` : services,
    })
  if (project.description)
    facts.push({ label: "Description", value: project.description })
  facts.push({
    label: "Created",
    value: (
      <time dateTime={project.createdAt}>{formatDate(project.createdAt)}</time>
    ),
  })
  return (
    <section
      aria-label="About this project"
      className="grid content-start gap-2.5"
    >
      <CloudSectionHead
        title="About"
        description="What Loomplane knows about it."
      />
      <FactList facts={facts} />
    </section>
  )
}

/**
 * Resources lists what runs as a resource, one row each, with its state in
 * every environment; a row opens the resource's page. It shows nothing once
 * every environment has answered with nothing deployed; an environment that
 * couldn't be read or can't be seen says so, so it never reads as empty.
 */
export function Resources({
  project,
  environments,
}: {
  project: Project
  environments?: Environment[]
}) {
  const list = useMemo(() => environments ?? [], [environments])
  const args = useMemo(
    () =>
      list.map((e) => ({
        path: { id: project.id },
        query: { environment: e.name },
      })),
    [list, project.id]
  )
  const { states, refetch } = useQueries(useBindingsList, args)
  // One row per resource, holding where it runs in each environment.
  const rows = new Map<string, Map<string, Binding>>()
  list.forEach((environment, i) => {
    for (const binding of catalogResources(states[i]?.data?.bindings ?? [])) {
      const row = rows.get(binding.name) ?? new Map<string, Binding>()
      row.set(environment.id, binding)
      rows.set(binding.name, row)
    }
  })
  const failures = list.flatMap((environment, i) => {
    const state = states[i]
    return state?.status === "error"
      ? [{ environment, error: state.error }]
      : []
  })
  const forbidden = (error: unknown) => [401, 403].includes(statusOf(error))
  const denied = failures.filter((f) => forbidden(f.error))
  const failed = failures.filter((f) => !forbidden(f.error))
  const loading = !environments || states.some((s) => s?.status === "pending")
  if (rows.size === 0 && failures.length === 0)
    return loading ? (
      <Skeleton
        role="status"
        aria-label="Loading resources"
        className="h-12 w-full rounded-xl"
      />
    ) : null
  const names = (items: { environment: Environment }[]) =>
    items.map((f) => f.environment.name).join(", ")
  return (
    <section aria-label="Resources" className="grid gap-2.5">
      <CloudSectionHead
        title="Resources"
        count={rows.size}
        description="Databases and caches the services need. Each deploys with the services that use it."
      />
      {failed.length > 0 && (
        <QueryError
          error={failed[0].error}
          fallback={`What runs in ${names(failed)} couldn't be loaded.`}
          retry={refetch}
        />
      )}
      {denied.length > 0 && (
        <CloudCallout
          tone="info"
          title={`Your role can't see what runs in ${names(denied)}.`}
        />
      )}
      {rows.size > 0 && (
        <CloudTable
          label="Resources"
          columns={["minmax(0,1.4fr)", ...list.map(() => "130px"), "80px"].join(
            " "
          )}
          head={["Resource", ...list.map((e) => e.name), ""]}
        >
          {[...rows].map(([name, byEnvironment]) => (
            <ResourceRow
              key={name}
              project={project}
              environments={list}
              byEnvironment={byEnvironment}
              unread={failures.map((f) => f.environment.id)}
            />
          ))}
        </CloudTable>
      )}
    </section>
  )
}

function ResourceRow({
  project,
  environments,
  byEnvironment,
  unread,
}: {
  project: Project
  environments: Environment[]
  /** The resource's binding in each environment it runs in, by ID. */
  byEnvironment: Map<string, Binding>
  /** Environments whose bindings couldn't be read. */
  unread: string[]
}) {
  // It opens on the first environment it runs in.
  const first = environments.find((e) => byEnvironment.has(e.id))
  const binding = first && byEnvironment.get(first.id)
  if (!first || !binding) return null
  const config = parseResourceConfig(binding)
  const kind = config?.composition?.kind ?? config?.kind ?? binding.engine
  const module = catalogKind(kind)
  const title = module?.title ?? (kind ? resourceLabel(kind) : "Resource")
  return (
    <CloudTableRow>
      <NameCell
        tile={
          <ProductIcon
            product={module?.logo}
            fallback={BoxIcon}
            label={title}
            size="sm"
          />
        }
        name={
          <Link
            to="/projects/$id/catalog/$resource"
            params={{ id: project.id, resource: binding.name }}
            search={{ environment: first.id }}
            className="text-foreground underline-offset-2 hover:underline"
          >
            {binding.name}
          </Link>
        }
        meta={title}
      />
      {environments.map((e) => {
        const b = byEnvironment.get(e.id)
        return (
          <Cell key={e.id}>
            {b ? (
              b.drift ? (
                <StatusPill tone="waiting">Changed outside</StatusPill>
              ) : (
                <StatusPill tone="live">Live</StatusPill>
              )
            ) : unread.includes(e.id) ? (
              <span className="text-muted-foreground">Unknown</span>
            ) : (
              <StatusPill tone="quiet">Not deployed</StatusPill>
            )}
          </Cell>
        )
      })}
      <ActionsCell>
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={
            <Link
              to="/projects/$id/catalog/$resource"
              params={{ id: project.id, resource: binding.name }}
              search={{ environment: first.id }}
            />
          }
        >
          Open
        </Button>
      </ActionsCell>
    </CloudTableRow>
  )
}

// Resource labels are names, except two common nouns that read wrong
// capitalized mid-sentence.
function needName(kind: string): string {
  if (kind === "object-storage") return "object storage"
  if (kind === "search") return "a search engine"
  return resourceLabel(kind)
}

/**
 * Discovered renders children with the project's graph, or says why there
 * isn't one yet: no repository, no analysis, or still loading.
 */
function Discovered({
  project,
  analyses,
  view,
  viewError,
  mayRun,
  mayEdit,
  children,
}: {
  project: Project
  analyses: ReturnType<typeof useAnalyses>
  view: ProjectView | null
  viewError?: unknown
  mayRun: boolean
  mayEdit: boolean
  children: (graph: Graph) => ReactNode
}) {
  if (project.repositories.length === 0) {
    return <ConnectRepository project={project} mayEdit={mayEdit} />
  }
  if (analyses.error) {
    return (
      <CloudCallout tone="error" title="Analyses couldn't be loaded">
        {messageOf(analyses.error, "Try again in a moment.")}
      </CloudCallout>
    )
  }
  // A failed poll keeps the graph already loaded; only say so when there is none.
  if (viewError && !view) {
    return (
      <CloudCallout tone="error" title="The project's graph couldn't be loaded">
        {messageOf(viewError, "Try again in a moment.")}
      </CloudCallout>
    )
  }
  if (!analyses.loaded || !view) {
    return <Skeleton className="h-72 w-full rounded-xl" />
  }
  const graph = view.graph
  if (!graph || !view.sources.some((s) => s.shown)) {
    return (
      <NotAnalyzed
        project={project}
        mayRun={mayRun}
        running={analyses.running}
      />
    )
  }
  return children(graph)
}

function Details({
  project,
  onCopy,
}: {
  project: Project
  onCopy: () => void
}) {
  return (
    <section aria-label="Details" className="grid gap-2.5">
      <CloudSectionHead
        title="Details"
        description="The slug never changes, because other systems key on it."
      />
      <FactList
        facts={[
          {
            label: "Repositories",
            value:
              project.repositories.length > 0 ? (
                <code className="font-mono text-[12.5px]">
                  {project.repositories.join(", ")}
                </code>
              ) : (
                <span className="text-muted-foreground">None yet</span>
              ),
          },
          {
            label: "Slug",
            value: (
              <code className="font-mono text-[12.5px]">{project.slug}</code>
            ),
          },
          {
            label: "Project ID",
            value: (
              <span className="inline-flex items-center gap-2">
                <code className="font-mono text-[12.5px] break-all">
                  {project.id}
                </code>
                <Button
                  variant="outline"
                  size="xs"
                  aria-label="Copy project ID"
                  onClick={onCopy}
                >
                  Copy
                </Button>
              </span>
            ),
          },
          {
            label: "Created",
            value: (
              <time dateTime={project.createdAt}>
                {formatDate(project.createdAt)}
              </time>
            ),
          },
        ]}
      />
    </section>
  )
}
