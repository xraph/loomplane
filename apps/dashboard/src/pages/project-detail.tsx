import { BoxIcon, RocketIcon, SparklesIcon } from "lucide-react"
import {
  Fragment,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useBindingsList,
  useEnvironmentsList,
  useProjectsDelete,
  useProjectsGet,
  useProjectsGraph,
  useProjectsUpdate,
  useProjectVariablesList,
} from "@/client/forge/loomplane/hooks"
import type {
  Analysis,
  Binding,
  Environment,
  Graph,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
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
  useAnalyses,
  useAnalyze,
} from "@/components/architecture"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudConfirm,
  CloudPageHeader,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { DatabaseGateways } from "@/components/database-gateway"
import { DeployTab, ManifestSettings } from "@/components/deploy"
import { AddEnvironment, Environments } from "@/components/environment-manager"
import { ProjectsIcon } from "@/components/icons"
import { Page } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import { ProjectGeneral } from "@/components/project-general"
import { ProjectServices } from "@/components/project-services"
import { ProjectStylePopover } from "@/components/project-style-picker"
import { ProjectVariables } from "@/components/project-variables"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import {
  type GraphSelection,
  needsReview,
  ServiceGraph,
} from "@/components/service-graph"
import { SiteHeader } from "@/components/site-header"
import { Repositories } from "@/components/sources"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import {
  analysisKey,
  ecosystemLabel,
  isActive,
  resourceLabel,
  shortSHA,
} from "@/lib/architecture"
import {
  catalogKind,
  catalogResources,
  parseResourceConfig,
} from "@/lib/catalog-kinds"
import { formatDate, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { projectStyle } from "@/lib/project-style"
import { isReady } from "@/lib/projects"
import { Link, useNavigate } from "@/lib/router"
import { useUrlTab } from "@/lib/use-url-tab"
import { useNow, useRuns } from "@/lib/use-runs"
import { ServiceRecovery } from "@/pages/service-catalog"

/** The project page's tabs, in order. Overview is the default. */
export type ProjectTab =
  | "overview"
  | "services"
  | "deployments"
  | "variables"
  | "analyses"
  | "backups"
  | "settings"

export function ProjectDetail({
  id,
  tab,
  environment,
}: {
  id: string
  /** The tab the page opens on, from ?tab=. */
  tab?: ProjectTab
  /** The environment a tab opens on, from ?environment=. */
  environment?: string
}) {
  const project = useQuery(useProjectsGet, { path: { id } })
  const missing = project.status === "error" && statusOf(project.error) === 404

  // The environments arrive from a background job; watch until it's done.
  useRefetchWhile(
    !!project.data && !isReady(project.data.status),
    project.refetch
  )

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/projects" },
          { label: project.data?.name ?? "Project" },
        ]}
      />
      <Page>
        {project.status === "error" &&
          (missing ? (
            <CloudEmpty
              kind="notice"
              icon={ProjectsIcon}
              title="No such project here"
              action={
                <Button nativeButton={false} render={<Link to="/projects" />}>
                  Back to projects
                </Button>
              }
            >
              It may have been deleted, or it belongs to another workspace. Pick
              one from the sidebar.
            </CloudEmpty>
          ) : (
            <QueryError
              error={project.error}
              fallback="The project couldn't be loaded."
              retry={project.refetch}
            />
          ))}
        {!project.data && project.status !== "error" && (
          <div className="grid gap-6" aria-busy="true">
            <Skeleton className="h-12 w-72" />
            <Skeleton className="h-9 w-full max-w-xl" />
            <Skeleton className="h-32 rounded-xl" />
            <Skeleton className="h-72 rounded-xl" />
          </div>
        )}
        {project.data && (
          <Loaded project={project.data} tab={tab} environment={environment} />
        )}
      </Page>
    </>
  )
}

function Loaded({
  project,
  tab,
  environment,
}: {
  project: Project
  tab?: ProjectTab
  environment?: string
}) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  const settingUp = !isReady(project.status)
  useRefetchWhile(settingUp, environments.refetch)
  const analyses = useAnalyses(project.id)

  // One graph for the whole page: the header's languages, the review
  // notices, every tab and the detail sheet all read it. It is joined
  // from the newest successful analysis of every repository, so it is
  // fetched again while any of them runs, and once more when the last
  // one stops, to pick up what it found.
  const view = useQuery(useProjectsGraph, { path: { id: project.id } })
  useRefetchWhile(analyses.running, view.refetch)
  useRefetchAfter(analyses.running, view.refetch)
  // An analysis can start and finish between two fetches of the list, so
  // nothing is ever seen running. Any change to a repository's newest
  // analysis means the graph is stale.
  useRefetchOnChange(
    analyses.loaded ? analysisKey(analyses.list) : undefined,
    view.refetch
  )

  return (
    <Body
      project={project}
      wanted={tab}
      wantedEnvironment={environment}
      environments={{
        list: environments.data?.environments,
        error: environments.status === "error" ? environments.error : undefined,
        refetch: environments.refetch,
      }}
      analyses={analyses}
      view={view.data ?? null}
      viewError={view.status === "error" ? view.error : undefined}
    />
  )
}

/** useRefetchAfter refetches once when active turns false, so the last tick isn't missed. */
function useRefetchAfter(active: boolean, refetch: () => Promise<unknown>) {
  const was = useRef(active)
  useEffect(() => {
    if (was.current && !active) refetch().catch(() => undefined)
    was.current = active
  }, [active, refetch])
}

/**
 * useRefetchOnChange refetches when key changes. The first key it sees,
 * once there is one, is the baseline and refetches nothing.
 */
function useRefetchOnChange(
  key: string | undefined,
  refetch: () => Promise<unknown>
) {
  const last = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (key === undefined) return
    const was = last.current
    last.current = key
    if (was !== undefined && was !== key) refetch().catch(() => undefined)
  }, [key, refetch])
}

type EnvironmentsRead = {
  /** Undefined until the list arrives. */
  list?: Environment[]
  error?: unknown
  refetch: () => Promise<unknown>
}

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

/** headState is the project's one status: set up, analyzed or not. */
function headState(
  project: Project,
  latest?: Analysis
): { label: string; tone: Tone } {
  if (!isReady(project.status)) return { label: "Setting up", tone: "flight" }
  if (latest?.status === "failed")
    return { label: "Analysis failed", tone: "failed" }
  return { label: "Ready", tone: "live" }
}

function Body({
  project,
  wanted,
  wantedEnvironment,
  environments,
  analyses,
  view,
  viewError,
}: {
  project: Project
  wanted?: ProjectTab
  wantedEnvironment?: string
  environments: EnvironmentsRead
  analyses: ReturnType<typeof useAnalyses>
  view: ProjectView | null
  viewError?: unknown
}) {
  const { can } = useOrganization()
  const navigate = useNavigate()
  const { runs } = useRuns()
  const ownRuns = runs.filter((r) => r.projectId === project.id)
  const update = useMutation(useProjectsUpdate)
  const variables = useQuery(useProjectVariablesList, {
    path: { id: project.id },
  })
  const [selected, setSelected] = useState<GraphSelection | null>(null)
  const [deleting, setDeleting] = useState(false)
  const now = useNow(60_000)
  const mayRun = can("analysis:run")
  const mayEdit = can("project:update")
  const mayBackups = can("service_backup:read")
  const hasRepositories = project.repositories.length > 0
  const analyze = useAnalyze(project, analyses.running)
  const graph = view?.graph ?? null
  // The header names one branch and commit, which only means something
  // when the graph was read from one analysis. The caption under the graph
  // names every commit.
  const shownAnalyses = (view?.sources ?? []).flatMap((s) =>
    s.shown ? [s.shown] : []
  )
  const shown = shownAnalyses.length === 1 ? shownAnalyses[0] : undefined
  const style = projectStyle(project)
  const ecosystems = [...new Set(graph?.services.map((s) => s.ecosystem))]
  const toReview = graph?.resources.filter(needsReview) ?? []
  const state = headState(project, analyses.latest)

  // A service project from the catalog has no repository to graph, so it
  // opens on its backups.
  const fallback: ProjectTab =
    project.icon === "database" && !hasRepositories && mayBackups
      ? "backups"
      : "overview"
  const [chosen, setTab] = useUrlTab(wanted, fallback)
  const waiting = ownRuns.filter((r) => r.status === "waiting").length
  const tabs: {
    id: ProjectTab
    label: string
    count?: number
    show: boolean
  }[] = [
    { id: "overview", label: "Overview", show: true },
    {
      id: "services",
      label: "Services",
      count: graph?.services.length,
      show: true,
    },
    {
      id: "deployments",
      label: "Deployments",
      // Deployments counts the plans waiting on someone, not every one.
      count: waiting || undefined,
      show: true,
    },
    {
      id: "variables",
      label: "Variables",
      count: variables.data?.variables.length,
      show: true,
    },
    {
      id: "analyses",
      label: "Analyses",
      count: analyses.list.length || undefined,
      show: true,
    },
    { id: "backups", label: "Backups", show: mayBackups },
    { id: "settings", label: "Settings", show: true },
  ]
  const tab = tabs.some((t) => t.id === chosen && t.show) ? chosen : fallback

  // Opens a tab on one environment, such as Variables for staging.
  const openOn = (next: ProjectTab, env: string) => {
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        tab: next === fallback ? undefined : next,
        environment: env,
      }),
      replace: true,
    } as never)
  }

  const restyle = (change: { icon?: string; color?: string }) => {
    update
      .mutateAsync({ path: { id: project.id }, body: change })
      .catch((err: unknown) =>
        toast.error(messageOf(err, "The icon and color weren't saved."))
      )
  }

  const copyID = () => {
    void navigator.clipboard?.writeText(project.id)
    toast.success("Copied the project ID")
  }

  const selectedService =
    selected?.kind === "service"
      ? graph?.services.find((s) => s.id === selected.id)
      : undefined
  const selectedResource =
    selected?.kind === "resource"
      ? graph?.resources.find((r) => r.id === selected.id)
      : undefined

  const meta: ReactNode[] = [
    hasRepositories
      ? plural(project.repositories.length, "repository", "repositories")
      : "No repository yet",
    shown && (
      <>
        {shown.source} on {shown.ref || "the default branch"}
        {shown.commitSha && (
          <>
            {" @ "}
            <span className="font-mono">{shortSHA(shown.commitSha)}</span>
          </>
        )}
      </>
    ),
    ecosystems.length > 0 && ecosystems.map(ecosystemLabel).join(", "),
  ].filter(Boolean)

  const discovered = (children: (graph: Graph) => ReactNode) => (
    <Discovered
      project={project}
      analyses={analyses}
      view={view}
      viewError={viewError}
      mayRun={mayRun}
      mayEdit={mayEdit}
    >
      {children}
    </Discovered>
  )

  return (
    <>
      <CloudPageHeader
        glyph={
          <ProjectStylePopover
            name={project.name}
            icon={style.icon}
            color={style.color}
            onChange={mayEdit ? restyle : undefined}
          />
        }
        title={project.name}
        status={<StatusPill tone={state.tone}>{state.label}</StatusPill>}
        meta={meta.map((m, i) => (
          <Fragment key={i}>
            {i > 0 && " · "}
            {m}
          </Fragment>
        ))}
        actions={
          <>
            {can("deployment:create") && isReady(project.status) && (
              <Button
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
            )}
            <PageMenu label="More project actions">
              {hasRepositories && mayRun && (
                <DropdownMenuItem
                  disabled={analyze.busy}
                  onClick={() =>
                    analyze.single ? analyze.choose() : analyze.run()
                  }
                >
                  {analyze.single
                    ? "Analyze a branch"
                    : "Analyze all repositories"}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem onClick={copyID}>
                Copy project ID
              </DropdownMenuItem>
              {can("project:delete") && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="destructive"
                    onClick={() => setDeleting(true)}
                  >
                    Delete project
                  </DropdownMenuItem>
                </>
              )}
            </PageMenu>
          </>
        }
      />
      {analyze.dialog}

      <Tabs value={tab} onValueChange={(v) => setTab(v as ProjectTab)}>
        <TabsList
          variant="line"
          aria-label="Project views"
          className="mb-5 max-w-full overflow-x-auto"
        >
          {tabs
            .filter((t) => t.show)
            .map((t) => (
              <TabsTrigger key={t.id} value={t.id}>
                {t.label}
                {t.count !== undefined && (
                  <TabsCount
                    className={
                      t.id === "deployments"
                        ? "bg-weld-tint text-weld-ink"
                        : undefined
                    }
                  >
                    {t.count}
                  </TabsCount>
                )}
              </TabsTrigger>
            ))}
        </TabsList>

        <TabsContent value="overview" className="grid gap-6">
          {analyses.latest && isActive(analyses.latest.status) && (
            <AnalysisBanner analysis={analyses.latest} />
          )}
          {analyses.latest?.status === "failed" && (
            <CloudCallout
              tone="error"
              title={`The latest analysis${analyses.latest.commitSha ? ` of ${shortSHA(analyses.latest.commitSha)}` : ""} failed`}
              action={
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setTab("analyses")}
                >
                  Analyses
                </Button>
              }
            >
              {analyses.latest.error}
            </CloudCallout>
          )}
          {toReview.map((r) => (
            <CloudCallout
              key={r.id}
              icon={SparklesIcon}
              title={`Loomplane thinks ${project.name} needs ${needName(r.kind)}, but isn't sure`}
              action={
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setSelected({ kind: "resource", id: r.id })}
                >
                  Review
                </Button>
              }
            >
              {r.evidence[0]
                ? `Read from ${r.evidence[0].path}${r.evidence[0].line ? `:${r.evidence[0].line}` : ""}. `
                : ""}
              Nothing is provisioned for it until someone confirms it.
            </CloudCallout>
          ))}

          <Environments
            project={project}
            environments={environments.list}
            error={environments.error}
            retry={environments.refetch}
            mayManage={can("environment:manage")}
            mayDeploy={can("deployment:create")}
            runs={ownRuns}
            onOpen={(next, env) => openOn(next, env)}
          />

          <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_300px]">
            <section aria-label="Service graph" className="grid gap-2.5">
              <CloudSectionHead
                title="How it fits together"
                description={graphNote(shown, view?.excluded.length ?? 0, now)}
              >
                {graph && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setTab("services")}
                  >
                    All services
                  </Button>
                )}
              </CloudSectionHead>
              {discovered((g) =>
                g.services.length + g.resources.length > 0 ? (
                  <>
                    <ServiceGraph
                      graph={g}
                      selected={selected}
                      onSelect={setSelected}
                    />
                    <GraphCaption graph={g} />
                  </>
                ) : (
                  <ServicesTable graph={g} onSelect={setSelected} />
                )
              )}
            </section>
            <About
              project={project}
              graph={graph}
              shown={shown}
              excluded={view?.excluded.length ?? 0}
            />
          </div>

          {hasRepositories && <Repositories project={project} />}
        </TabsContent>

        <TabsContent value="services" className="grid gap-6">
          {discovered((g) =>
            view ? (
              <ProjectServices
                project={project}
                environments={environments.list ?? []}
                view={view}
              />
            ) : (
              <ServicesTable graph={g} onSelect={setSelected} />
            )
          )}
          <Resources project={project} environments={environments.list} />
          {graph && view?.sources.some((s) => s.shown) && (
            <Structure sources={graph.sources ?? []} graph={graph} />
          )}
          <DatabaseGateways project={project.id} />
        </TabsContent>

        <TabsContent value="deployments">
          <DeployTab key={project.id} project={project} />
        </TabsContent>

        <TabsContent value="variables">
          {environments.error !== undefined && !environments.list ? (
            <QueryError
              error={environments.error}
              fallback="The environments couldn't be loaded."
              retry={environments.refetch}
            />
          ) : !environments.list ? (
            <Skeleton className="h-32 rounded-xl" />
          ) : environments.list.length === 0 ? (
            <CloudEmpty
              title="No environments yet"
              action={
                can("environment:manage") && (
                  <AddEnvironment project={project} />
                )
              }
            >
              Variables are set per environment. Add one, such as production, to
              set them.
            </CloudEmpty>
          ) : (
            <ProjectVariables
              key={wantedEnvironment}
              project={project}
              environments={environments.list}
              environment={wantedEnvironment}
            />
          )}
        </TabsContent>

        <TabsContent value="analyses">
          <section aria-label="Analyses" className="grid gap-2.5">
            <CloudSectionHead
              title="Analyses"
              count={analyses.loaded ? analyses.list.length : undefined}
              description="Each time Loomplane read a branch to find what runs. The newest one draws the graph."
            >
              {hasRepositories && mayRun && (
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
        </TabsContent>

        {mayBackups && (
          <TabsContent value="backups" className="grid gap-6">
            <ServiceRecovery project={project.id} />
          </TabsContent>
        )}

        <TabsContent value="settings" className="grid max-w-[760px] gap-6">
          <ProjectGeneral
            key={`${project.id}:${project.updatedAt}`}
            project={project}
            mayEdit={mayEdit}
          />
          <Details project={project} onCopy={copyID} />
          <ManifestSettings project={project} />
          {can("project:delete") && (
            <DangerZone
              project={project}
              environments={environments.list}
              onDelete={() => setDeleting(true)}
            />
          )}
        </TabsContent>
      </Tabs>

      {can("project:delete") && (
        <DeleteProject
          project={project}
          environments={environments.list}
          open={deleting}
          onOpenChange={setDeleting}
        />
      )}

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

/**
 * useDeployed counts what runs from a project across its environments, so
 * deleting it says what is left running.
 */
function useDeployed(project: Project, environments?: Environment[]) {
  const list = useMemo(() => environments ?? [], [environments])
  const args = useMemo(
    () =>
      list.map((e) => ({
        path: { id: project.id },
        query: { environment: e.name },
      })),
    [list, project.id]
  )
  const { states } = useQueries(useBindingsList, args)
  let services = 0
  let resources = 0
  let places = 0
  for (const s of states) {
    const bindings = s?.data?.bindings ?? []
    const svc = bindings.filter((b) => b.subject === "service").length
    services += svc
    resources += bindings.length - svc
    if (bindings.length > 0) places++
  }
  return {
    loaded: !!environments && states.every((s) => s?.status === "success"),
    failed: states.some((s) => s?.status === "error"),
    services,
    resources,
    places,
  }
}

/** deployedSummary says what runs from a project, for its delete copy. */
function deployedSummary(d: ReturnType<typeof useDeployed>): string {
  if (d.failed)
    return "What runs from it couldn't all be read. Anything deployed keeps running until you remove it in its account."
  if (!d.loaded) return "Checking what runs from it."
  if (d.services + d.resources === 0)
    return "Nothing runs from it yet, so no cloud resources are touched."
  const what = [
    d.services > 0 && plural(d.services, "service"),
    d.resources > 0 && plural(d.resources, "resource"),
  ]
    .filter(Boolean)
    .join(" and ")
  return `${what} ${d.services + d.resources === 1 ? "runs" : "run"} in ${plural(d.places, "environment")}. Deleting stops managing them; what runs keeps running until you remove it in each account.`
}

function DangerZone({
  project,
  environments,
  onDelete,
}: {
  project: Project
  environments?: Environment[]
  onDelete: () => void
}) {
  const deployed = useDeployed(project, environments)
  return (
    <section aria-label="Danger zone" className="grid gap-2.5">
      <CloudSectionHead
        title="Danger zone"
        description="Each asks you to confirm in a dialog before anything happens."
      />
      <div className="flex flex-wrap items-center gap-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-destructive/25">
        <div className="grid min-w-0 flex-[1_1_360px] gap-1">
          <p className="text-[13px] font-semibold">Delete this project</p>
          <p className="text-[12.5px] leading-normal text-muted-foreground">
            {deployedSummary(deployed)}
          </p>
        </div>
        <Button
          variant="outline"
          className="border-destructive/25 text-destructive hover:bg-destructive/10 hover:text-destructive"
          onClick={onDelete}
        >
          Delete project
        </Button>
      </div>
    </section>
  )
}

function DeleteProject({
  project,
  environments,
  open,
  onOpenChange,
}: {
  project: Project
  environments?: Environment[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const remove = useMutation(useProjectsDelete)
  const deployed = useDeployed(project, open ? environments : undefined)
  const [error, setError] = useState("")

  const confirm = async () => {
    setError("")
    try {
      // The delete invalidates the project list for us.
      await remove.mutateAsync({ path: { id: project.id } })
      toast.success(`Deleted ${project.name}`)
      onOpenChange(false)
      void navigate({ to: "/projects", replace: true })
    } catch (err) {
      setError(messageOf(err, "The project wasn't deleted."))
    }
  }

  return (
    <CloudConfirm
      open={open}
      onOpenChange={(next) => {
        if (!next) setError("")
        onOpenChange(next)
      }}
      title={`Delete ${project.name}?`}
      confirmLabel="Delete project"
      typeToConfirm={project.name}
      pending={remove.isPending}
      onConfirm={() => void confirm()}
      error={error && <CloudCallout tone="error" title={error} />}
    >
      Its environments and their variables go with it, and this can&apos;t be
      undone. {deployedSummary(deployed)}
    </CloudConfirm>
  )
}
