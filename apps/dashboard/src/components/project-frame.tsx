// The frame around every page of a project: who it is, its views, and the
// environment the page is about. The project's overview, one of its
// services, a deployment and a database all open inside it, so the way
// back to the rest of the project is always in the same place.
import { ChevronDownIcon, ClipboardCheckIcon, RocketIcon } from "lucide-react"
import {
  createContext,
  Fragment,
  type ReactNode,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useProjectsGet,
  useProjectsGraph,
  useProjectsUpdate,
  useProjectVariablesList,
} from "@/client/forge/loomplane/hooks"
import type {
  Analysis,
  Environment,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
import { useAnalyses, useAnalyze } from "@/components/architecture"
import {
  CloudPageHeader,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import { CloudEmpty } from "@/components/cloud-targets/infrastructure-ui"
import { ProjectsIcon } from "@/components/icons"
import { Page } from "@/components/page"
import {
  PrepareRelease,
  usePrepareRelease,
} from "@/components/prepare-release-dialog"
import { DeleteProject } from "@/components/project-delete"
import { ProjectStylePopover } from "@/components/project-style-picker"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { type Crumb, SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { ButtonGroup } from "@/components/ui/button-group"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { AddServiceProvider } from "@/lib/add-service-context"
import { analysisKey, ecosystemLabel, shortSHA } from "@/lib/architecture"
import { deployButtonMarkdown } from "@/lib/deploy-button"
import { repositoryKey } from "@/lib/format"
import { useProjectCan } from "@/lib/project-permissions"
import { chosenEnvironment, type ProjectTab } from "@/lib/project-nav"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { isReady } from "@/lib/projects"
import { Link, useNavigate } from "@/lib/router"
import type { Run } from "@/lib/runs"
import { useRuns } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

type EnvironmentsRead = {
  /** Undefined until the list arrives. */
  list?: Environment[]
  error?: unknown
  refetch: () => Promise<unknown>
}

/** What every page inside a project reads from its frame. */
export type ProjectFrameValue = {
  project: Project
  environments: EnvironmentsRead
  /**
   * The environment the page is about: the one in ?environment=, else
   * production, else the first. Undefined while they load or when the
   * project has none.
   */
  environment?: Environment
  /** Moves every environment-scoped view to another environment. */
  setEnvironment: (name: string) => void
  analyses: ReturnType<typeof useAnalyses>
  /** The project's graph, joined from every repository's newest analysis. */
  view: ProjectView | null
  viewError?: unknown
  refetchView: () => Promise<unknown>
  /** The project's runs, newest first. */
  runs: Run[]
  /** Opens the dialog that deletes the project. */
  requestDelete: () => void
}

const FrameContext = createContext<ProjectFrameValue | null>(null)

/** useProjectFrame reads the project a page is in. Only inside ProjectFrame. */
export function useProjectFrame(): ProjectFrameValue {
  const value = useContext(FrameContext)
  if (!value) throw new Error("useProjectFrame is used outside ProjectFrame")
  return value
}

/**
 * useOptionalProjectFrame is the project a page is in, or null for a page
 * that also opens on its own, such as a deployment opened by its ID.
 */
export function useOptionalProjectFrame(): ProjectFrameValue | null {
  return useContext(FrameContext)
}

/**
 * FramedPage is a Page for a page that also opens on its own: inside a
 * project's frame, whose page already holds it, it adds nothing.
 */
export function FramedPage({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  if (useOptionalProjectFrame()) return <>{children}</>
  return <Page className={className}>{children}</Page>
}

/**
 * useFramedCrumbs is the trail inside a project's frame when there is one,
 * else the fallback a page used on its own.
 */
export function useFramedCrumbs(rest: Crumb[], fallback: Crumb[]): Crumb[] {
  const frame = useOptionalProjectFrame()
  return frame ? projectCrumbs(frame.project, ...rest) : fallback
}

/**
 * projectCrumbs is the trail to a page inside a project: Projects, the
 * project, then the page's own.
 */
export function projectCrumbs(project: Project, ...rest: Crumb[]): Crumb[] {
  return [
    { label: "Projects", to: "/projects" },
    {
      label: project.name,
      link: <Link to="/projects/$id" params={{ id: project.id }} />,
    },
    ...rest,
  ]
}

/** Where in the project a page is, as its route says. */
export type FramePlace = {
  /** The view the nav marks, if any: a service's page marks Services. */
  tab?: ProjectTab
  /**
   * The project's own page, which carries its full header and actions.
   * The pages under it carry their own and show the project smaller.
   */
  index?: boolean
  /** Whether the page shows one environment, so the nav offers a choice. */
  environmentScoped?: boolean
}

export function ProjectFrame({
  id,
  environment,
  place,
  children,
}: {
  id: string
  /** The environment's name from ?environment=. */
  environment?: string
  place: FramePlace
  children: ReactNode
}) {
  const project = useQuery(useProjectsGet, { path: { id } })
  const missing = project.status === "error" && statusOf(project.error) === 404

  // The environments arrive from a background job; watch until it's done.
  useRefetchWhile(
    !!project.data && !isReady(project.data.status),
    project.refetch
  )

  if (!project.data)
    return (
      <>
        <SiteHeader
          crumbs={[
            { label: "Projects", to: "/projects" },
            { label: "Project" },
          ]}
        />
        <Page>
          {project.status === "error" ? (
            missing ? (
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
                It may have been deleted, or it belongs to another workspace.
                Pick one from the sidebar.
              </CloudEmpty>
            ) : (
              <QueryError
                error={project.error}
                fallback="The project couldn't be loaded."
                retry={project.refetch}
              />
            )
          ) : (
            <div className="grid gap-6" aria-busy="true">
              <Skeleton className="h-12 w-72" />
              <Skeleton className="h-9 w-full max-w-xl" />
              <Skeleton className="h-32 rounded-xl" />
              <Skeleton className="h-72 rounded-xl" />
            </div>
          )}
        </Page>
      </>
    )

  return (
    <Page>
      <Loaded project={project.data} environment={environment} place={place}>
        {children}
      </Loaded>
    </Page>
  )
}

function Loaded({
  project,
  environment,
  place,
  children,
}: {
  project: Project
  environment?: string
  place: FramePlace
  children: ReactNode
}) {
  const navigate = useNavigate()
  const can = useProjectCan(project.id)
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  useRefetchWhile(!isReady(project.status), environments.refetch)
  const analyses = useAnalyses(project.id)

  // One graph for every page of the project: the header's languages, the
  // nav's count, the overview and the services all read it. It is joined
  // from the newest successful analysis of every repository, so it is
  // fetched again while any of them runs, and once more when the last one
  // stops, to pick up what it found.
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

  const { runs } = useRuns()
  const ownRuns = runs.filter((r) => r.projectId === project.id)
  const [deleting, setDeleting] = useState(false)
  const list = environments.data?.environments
  const chosen = list ? chosenEnvironment(list, environment) : undefined

  const setEnvironment = (name: string) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        environment: name,
      }),
    } as never)

  const value: ProjectFrameValue = {
    project,
    environments: {
      list,
      error: environments.status === "error" ? environments.error : undefined,
      refetch: environments.refetch,
    },
    environment: chosen,
    setEnvironment,
    analyses,
    view: view.data ?? null,
    viewError: view.status === "error" ? view.error : undefined,
    refetchView: view.refetch,
    runs: ownRuns,
    requestDelete: () => setDeleting(true),
  }

  return (
    <FrameContext.Provider value={value}>
      <AddServiceProvider
        project={project}
        environments={list ?? []}
        enabled={can("project:update") && (list?.length ?? 0) > 0}
      >
        {place.index ? <FullHeader /> : <SmallHeader />}
        <ProjectNav place={place} environment={environment} />
        {children}
        {can("project:delete") && (
          <DeleteProject
            project={project}
            environments={list}
            open={deleting}
            onOpenChange={setDeleting}
          />
        )}
      </AddServiceProvider>
    </FrameContext.Provider>
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

/**
 * FullHeader heads the project's own page: its name, what it's built
 * from, and the actions on the whole project. Deploy leads them; adding a
 * service lives on the Services view and in the setup steps.
 */
function FullHeader() {
  const { project, environments, analyses, view, requestDelete } =
    useProjectFrame()
  const can = useProjectCan(project.id)
  const update = useMutation(useProjectsUpdate)
  const analyze = useAnalyze(project, analyses.running)
  const style = projectStyle(project)
  const graph = view?.graph ?? null
  const hasRepositories = project.repositories.length > 0
  // The header names one branch and commit, which only means something
  // when the graph was read from one analysis.
  const shownAnalyses = (view?.sources ?? []).flatMap((s) =>
    s.shown ? [s.shown] : []
  )
  const shown = shownAnalyses.length === 1 ? shownAnalyses[0] : undefined
  const ecosystems = [...new Set(graph?.services.map((s) => s.ecosystem))]
  const state = headState(project, analyses.latest)

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

  const meta: ReactNode[] = [
    hasRepositories
      ? plural(project.repositories.length, "repository", "repositories")
      : project.kind === "service"
        ? "From the catalog"
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

  const fleet = project.deploymentMode === "fleet"
  return (
    <>
      <CloudPageHeader
        glyph={
          <ProjectStylePopover
            name={project.name}
            icon={style.icon}
            color={style.color}
            onChange={can("project:update") ? restyle : undefined}
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
            {fleet && (
              <>
                <Button
                  variant="outline"
                  nativeButton={false}
                  render={
                    <Link
                      to="/projects/$id/fleet"
                      params={{ id: project.id }}
                    />
                  }
                >
                  Instance fleet
                </Button>
                <PrepareRelease
                  project={project}
                  environments={environments.list}
                  may={can("deployment:create")}
                />
              </>
            )}
            {!fleet && can("deployment:create") && isReady(project.status) && (
              <DeployButton
                project={project}
                environments={environments.list}
              />
            )}
            <PageMenu label="More project actions">
              {hasRepositories && can("analysis:run") && (
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
              {hasRepositories && (
                <CopyDeployButton repositories={project.repositories} />
              )}
              {can("project:delete") && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="destructive"
                    onClick={requestDelete}
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
    </>
  )
}

/**
 * CopyDeployButton copies the Markdown for a "Deploy with Loomplane"
 * button, for a repository's README. The button starts a project from one
 * repository, so a project built from several offers one per repository.
 */
function CopyDeployButton({ repositories }: { repositories: string[] }) {
  const copy = (repository: string) => {
    const name = repositoryKey(repository) || repository
    void navigator.clipboard?.writeText(
      deployButtonMarkdown(window.location.origin, repository)
    )
    toast.success("Copied the deploy button", {
      description: `Paste it into the README of ${name}. Anyone who clicks it can start a project from ${name} on this Loomplane.`,
    })
  }
  if (repositories.length === 1)
    return (
      <DropdownMenuItem onClick={() => copy(repositories[0])}>
        Copy deploy button
      </DropdownMenuItem>
    )
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>Copy deploy button</DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        <DropdownMenuGroup>
          <DropdownMenuLabel>For the README of</DropdownMenuLabel>
          {repositories.map((r) => (
            <DropdownMenuItem key={r} onClick={() => copy(r)}>
              <span className="font-mono text-[12.5px]">
                {repositoryKey(r) || r}
              </span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}

/**
 * DeployButton plans a deployment of the whole project. Its menu deploys
 * to one environment, or prepares a release from staging to production
 * where the agent can.
 */
function DeployButton({
  project,
  environments,
}: {
  project: Project
  environments?: Environment[]
}) {
  const prepare = usePrepareRelease({ project, environments, may: true })
  const list = environments ?? []
  return (
    <>
      <ButtonGroup>
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
        {(list.length > 1 || prepare.available) && (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  size="icon"
                  aria-label="More ways to deploy"
                  className="border-l border-primary-foreground/20"
                />
              }
            >
              <ChevronDownIcon />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-60">
              {list.length > 1 && (
                <DropdownMenuGroup>
                  <DropdownMenuLabel>
                    Deploy to one environment
                  </DropdownMenuLabel>
                  {list.map((e) => (
                    <DropdownMenuItem
                      key={e.id}
                      render={
                        <Link
                          to="/deployments/new"
                          search={{ project: project.id, environment: e.name }}
                        />
                      }
                    >
                      {e.name}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
              )}
              {prepare.available && (
                <>
                  {list.length > 1 && <DropdownMenuSeparator />}
                  <DropdownMenuItem onClick={prepare.open}>
                    <ClipboardCheckIcon />
                    Prepare a release
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </ButtonGroup>
      {prepare.dialog}
    </>
  )
}

/** SmallHeader names the project above a page of its own. */
function SmallHeader() {
  const { project, analyses } = useProjectFrame()
  const style = projectStyle(project)
  const state = headState(project, analyses.latest)
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <ProjectIcon icon={style.icon} color={style.color} size="sm" />
      <Link
        to="/projects/$id"
        params={{ id: project.id }}
        className="truncate text-[14px] font-semibold text-foreground hover:underline hover:underline-offset-4"
      >
        {project.name}
      </Link>
      {state.tone !== "live" && (
        <StatusPill tone={state.tone}>{state.label}</StatusPill>
      )}
    </div>
  )
}

/**
 * ProjectNav is the project's views, the same on every page of it, with
 * the environment the page shows where that's one environment.
 */
function ProjectNav({
  place,
  environment: wanted,
}: {
  place: FramePlace
  environment?: string
}) {
  const { project, environments, environment, setEnvironment, view, runs } =
    useProjectFrame()
  const variables = useQuery(useProjectVariablesList, {
    path: { id: project.id },
  })
  const waiting = runs.filter((r) => r.status === "waiting").length
  const tabs: {
    id: ProjectTab
    label: string
    count?: number
    weld?: boolean
  }[] = [
    { id: "overview", label: "Overview" },
    { id: "services", label: "Services", count: view?.graph?.services.length },
    // Deployments counts the plans waiting on someone, not every one.
    {
      id: "deployments",
      label: "Deployments",
      count: waiting || undefined,
      weld: true,
    },
    {
      id: "variables",
      label: "Variables",
      count: variables.data?.variables.length,
    },
    { id: "settings", label: "Settings" },
  ]
  const list = environments.list ?? []
  const showEnvironments =
    !!place.environmentScoped && list.length > 0 && !!environment

  return (
    <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2 border-b">
      <nav
        aria-label="Project views"
        className="-mb-px flex max-w-full gap-0.5 overflow-x-auto"
      >
        {tabs.map((t) => {
          const on = place.tab === t.id
          return (
            <Link
              key={t.id}
              to="/projects/$id"
              params={{ id: project.id }}
              search={{
                ...(t.id === "overview" ? {} : { tab: t.id }),
                // The environment carries over to the views that show one.
                ...(wanted && t.id === "variables"
                  ? { environment: wanted }
                  : {}),
              }}
              aria-current={on ? "page" : undefined}
              className={cn(
                "group/nav relative inline-flex h-[38px] shrink-0 items-center gap-1.5 px-3 text-[13.5px] font-medium whitespace-nowrap transition-colors",
                "after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-primary after:opacity-0 after:transition-opacity",
                "focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none",
                on
                  ? "text-foreground after:opacity-100"
                  : "text-foreground/60 hover:text-foreground dark:text-muted-foreground dark:hover:text-foreground"
              )}
            >
              {t.label}
              {t.count !== undefined && (
                <span
                  className={cn(
                    "min-w-[18px] rounded-full px-1.5 text-center font-mono text-[11px] tabular-nums",
                    t.weld
                      ? "bg-weld-tint text-weld-ink"
                      : on
                        ? "bg-primary text-primary-foreground"
                        : "bg-muted text-muted-foreground"
                  )}
                >
                  {t.count}
                </span>
              )}
            </Link>
          )
        })}
      </nav>
      {showEnvironments && (
        <div className="flex items-center gap-2 pb-1.5">
          <span className="text-[12.5px] text-muted-foreground">
            Environment
          </span>
          <Select
            items={list.map((e) => ({ label: e.name, value: e.name }))}
            value={environment.name}
            onValueChange={(v) => setEnvironment(v as string)}
          >
            <SelectTrigger size="sm" aria-label="Environment" className="w-44">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {list.map((e) => (
                <SelectItem key={e.id} value={e.name}>
                  {e.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
    </div>
  )
}
