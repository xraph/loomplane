import {
  CopyIcon,
  FolderGit2Icon,
  GitBranchIcon,
  GitCommitHorizontalIcon,
  RocketIcon,
  SparklesIcon,
} from "lucide-react"
import { type ReactNode, useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useProjectsDelete,
  useProjectsGet,
  useProjectsGraph,
  useProjectsUpdate,
} from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  Graph,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
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
} from "@/components/architecture"
import { DeployTab } from "@/components/deploy"
import { EnvironmentCards } from "@/components/environment-manager"
import { Page, PageHeading } from "@/components/page"
import { ProjectStylePopover } from "@/components/project-style-picker"
import { ProjectVariables } from "@/components/project-variables"
import {
  type GraphSelection,
  needsReview,
  ServiceGraph,
} from "@/components/service-graph"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { SourcesCard } from "@/components/sources"
import { isReady } from "@/lib/projects"
import { useRuns } from "@/lib/use-runs"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  analysisKey,
  ecosystemLabel,
  resourceLabel,
  shortSHA,
} from "@/lib/architecture"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

export function ProjectDetail({ id }: { id: string }) {
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
            <Empty className="border border-dashed">
              <EmptyHeader>
                <EmptyTitle>No such project here</EmptyTitle>
                <EmptyDescription>
                  It may have been deleted, or it belongs to another
                  organization. Pick one from the sidebar.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Link to="/projects" className={buttonVariants()}>
                  Back to projects
                </Link>
              </EmptyContent>
            </Empty>
          ) : (
            <Alert variant="destructive">
              <AlertDescription>
                {messageOf(project.error, "The project couldn't be loaded.")}
              </AlertDescription>
            </Alert>
          ))}
        {!project.data && project.status !== "error" && (
          <div className="grid gap-6" aria-busy="true">
            <Skeleton className="h-10 w-72" />
            <div className="grid gap-4 md:grid-cols-2">
              <Skeleton className="h-28 rounded-xl" />
              <Skeleton className="h-28 rounded-xl" />
            </div>
            <Skeleton className="h-72 rounded-xl" />
          </div>
        )}
        {project.data && <Loaded project={project.data} />}
      </Page>
    </>
  )
}

function Loaded({ project }: { project: Project }) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  const settingUp = !isReady(project.status)
  useRefetchWhile(settingUp, environments.refetch)
  const analyses = useAnalyses(project.id)

  // One graph for the whole page: the header's languages, the review
  // banners, every tab and the detail sheet all read it. It is joined
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
      environments={environments.data?.environments}
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

function Body({
  project,
  environments,
  analyses,
  view,
  viewError,
}: {
  project: Project
  environments?: Environment[]
  analyses: ReturnType<typeof useAnalyses>
  view: ProjectView | null
  viewError?: unknown
}) {
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const { runs } = useRuns()
  const ownRuns = runs.filter((r) => r.projectId === project.id)
  const update = useMutation(useProjectsUpdate)
  const [selected, setSelected] = useState<GraphSelection | null>(null)
  const mayRun = can("analysis:run")
  const mayEdit = can("project:update")
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

  const restyle = (change: { icon?: string; color?: string }) => {
    update
      .mutateAsync({ path: { id: project.id }, body: change })
      .catch((err: unknown) =>
        toast.error(messageOf(err, "The icon and color weren't saved."))
      )
  }

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
      <PageHeading
        title={
          <span className="flex items-center gap-3">
            <ProjectStylePopover
              name={project.name}
              icon={style.icon}
              color={style.color}
              onChange={mayEdit ? restyle : undefined}
            />
            <span className="min-w-0 wrap-break-word">{project.name}</span>
          </span>
        }
        description={project.description || undefined}
        actions={
          <>
            {project.repositories.length > 0 && mayRun && (
              <AnalyzeActions project={project} running={analyses.running} />
            )}
            {can("deployment:create") && isReady(project.status) && (
              <Button
                className="rounded-full"
                onClick={() => openDeploy({ project: project.id })}
              >
                <RocketIcon />
                Deploy
              </Button>
            )}
          </>
        }
      >
        <div className="mt-2 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          {project.repositories.length > 0 ? (
            project.repositories.map((r) => (
              <Badge
                key={r}
                variant="outline"
                className="h-6 gap-1.5 font-normal"
                render={
                  <a
                    href={`https://github.com/${r}`}
                    target="_blank"
                    rel="noreferrer"
                  />
                }
              >
                <FolderGit2Icon />
                {r}
              </Badge>
            ))
          ) : (
            <Badge variant="outline" className="h-6 gap-1.5 font-normal">
              <FolderGit2Icon />
              No repository yet
            </Badge>
          )}
          {shown?.ref && (
            <Badge variant="outline" className="h-6 gap-1.5 font-normal">
              <GitBranchIcon />
              {shown.ref}
            </Badge>
          )}
          {shown?.commitSha && (
            <Badge
              variant="outline"
              className="h-6 gap-1.5 font-mono font-normal"
            >
              <GitCommitHorizontalIcon />
              {shortSHA(shown.commitSha)}
            </Badge>
          )}
          {ecosystems.map((e) => (
            <Badge key={e} variant="secondary" className="font-normal">
              {ecosystemLabel(e)}
            </Badge>
          ))}
          {!isReady(project.status) && (
            <Badge variant="outline" className="h-6 gap-1.5 font-normal">
              <span className="h-1.5 w-3 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
              Setting up
            </Badge>
          )}
        </div>
      </PageHeading>

      <EnvironmentCards
        project={project}
        environments={environments ?? []}
        loading={!environments}
        mayManage={can("environment:manage")}
        runs={ownRuns}
      />

      {analyses.latest && <AnalysisBanner analysis={analyses.latest} />}

      {toReview.map((r) => (
        <Alert
          key={r.id}
          className="items-center gap-y-1 rounded-xl border-dashed border-weld/60 bg-[color-mix(in_oklch,var(--weld)_5%,var(--card))] p-4 has-[>svg]:gap-x-3 md:has-[>svg]:grid-cols-[auto_1fr_auto]"
        >
          <SparklesIcon className="text-weld-ink size-5!" />
          <AlertTitle>
            Loomplane thinks {project.name} needs {needName(r.kind)}, but
            isn&apos;t sure
          </AlertTitle>
          <AlertDescription className="col-start-2">
            {r.evidence[0]
              ? `Read from ${r.evidence[0].path}${r.evidence[0].line ? `:${r.evidence[0].line}` : ""}. `
              : ""}
            Nothing is provisioned for it until someone confirms it.
          </AlertDescription>
          <div className="col-start-2 mt-2 flex gap-2 md:col-start-3 md:row-span-2 md:row-start-1 md:mt-0">
            <Button
              variant="outline"
              className="rounded-full"
              onClick={() => setSelected({ kind: "resource", id: r.id })}
            >
              Review
            </Button>
          </div>
        </Alert>
      ))}

      <Tabs defaultValue="graph">
        <TabsList
          variant="line"
          className="mb-2 max-w-full justify-start overflow-x-auto"
        >
          <TabsTrigger value="graph">Graph</TabsTrigger>
          <TabsTrigger value="services">
            Services
            {graph && (
              <span className="text-muted-foreground tabular-nums">
                {graph.services.length}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="variables">Variables</TabsTrigger>
          <TabsTrigger value="deploy">Deploy</TabsTrigger>
          <TabsTrigger value="analyses">
            Analyses
            {analyses.list.length > 0 && (
              <span className="text-muted-foreground tabular-nums">
                {analyses.list.length}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>
        <TabsContent value="graph" className="grid gap-3">
          {project.repositories.length > 0 && <SourcesCard project={project} />}
          <Discovered
            project={project}
            analyses={analyses}
            view={view}
            viewError={viewError}
            mayRun={mayRun}
            mayEdit={mayEdit}
          >
            {(g) =>
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
            }
          </Discovered>
        </TabsContent>
        <TabsContent value="services" className="grid gap-4">
          <Discovered
            project={project}
            analyses={analyses}
            view={view}
            viewError={viewError}
            mayRun={mayRun}
            mayEdit={mayEdit}
          >
            {(g) => (
              <>
                <ServicesTable graph={g} onSelect={setSelected} />
                <Structure sources={g.sources ?? []} graph={g} />
              </>
            )}
          </Discovered>
        </TabsContent>
        <TabsContent value="variables">
          {environments && environments.length > 0 ? (
            <ProjectVariables project={project} environments={environments} />
          ) : (
            <Skeleton className="h-40 w-full rounded-xl" />
          )}
        </TabsContent>
        <TabsContent value="deploy">
          <DeployTab
            project={project}
            environments={environments ?? []}
            analysis={analyses.shown}
          />
        </TabsContent>
        <TabsContent value="analyses">
          {analyses.error ? (
            <Alert variant="destructive">
              <AlertDescription>
                {messageOf(analyses.error, "Analyses couldn't be loaded.")}
              </AlertDescription>
            </Alert>
          ) : (
            <AnalysisHistory analyses={analyses.list} />
          )}
        </TabsContent>
        <TabsContent value="settings" className="grid gap-6">
          <Details project={project} />
          {can("project:delete") && <DeleteProject project={project} />}
        </TabsContent>
      </Tabs>

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
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(analyses.error, "Analyses couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  }
  // A failed poll keeps the graph already loaded; only say so when there is none.
  if (viewError && !view) {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(viewError, "The project's graph couldn't be loaded.")}
        </AlertDescription>
      </Alert>
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

function Details({ project }: { project: Project }) {
  const copy = (value: string, what: string) => () => {
    void navigator.clipboard?.writeText(value)
    toast.success(`Copied the ${what}`)
  }
  const rows: [string, ReactNode][] = [
    [
      "Repositories",
      project.repositories.length > 0 ? (
        <code className="font-mono text-xs">
          {project.repositories.join(", ")}
        </code>
      ) : (
        <span className="text-muted-foreground">None yet</span>
      ),
    ],
    ["Slug", <code className="font-mono text-xs">{project.slug}</code>],
    [
      "Project ID",
      <span className="flex items-center gap-1">
        <code className="font-mono text-xs break-all">{project.id}</code>
        <Button
          variant="ghost"
          size="icon-xs"
          className="text-muted-foreground"
          aria-label="Copy project ID"
          onClick={copy(project.id, "project ID")}
        >
          <CopyIcon />
        </Button>
      </span>,
    ],
    [
      "Created",
      <time dateTime={project.createdAt}>{formatDate(project.createdAt)}</time>,
    ],
  ]
  return (
    <Card>
      <CardHeader>
        <CardTitle>Details</CardTitle>
        <CardDescription>
          The slug never changes, because other systems key on it.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-3 text-sm">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="min-w-0">{v}</dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  )
}

function DeleteProject({ project }: { project: Project }) {
  const navigate = useNavigate()
  const remove = useMutation(useProjectsDelete)
  // Controlled: Base UI's AlertDialogAction is a plain button and doesn't
  // close the dialog, so a failed delete closes it here.
  const [open, setOpen] = useState(false)

  const confirm = async () => {
    try {
      // The delete invalidates the project list for us.
      await remove.mutateAsync({ path: { id: project.id } })
      toast.success(`Deleted ${project.name}`)
      void navigate({ to: "/projects", replace: true })
    } catch (err) {
      setOpen(false)
      toast.error(messageOf(err, "The project wasn't deleted."))
    }
  }

  return (
    <Card className={cn("border-destructive/40")}>
      <CardHeader>
        <CardTitle>Delete project</CardTitle>
        <CardDescription>
          Removes the project and its environments from Loomplane. Nothing has
          been deployed from it yet, so no cloud resources are touched.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <AlertDialog open={open} onOpenChange={setOpen}>
          <AlertDialogTrigger
            render={
              <Button
                variant="destructive"
                className="rounded-full"
                disabled={remove.isPending}
              />
            }
          >
            Delete project
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete {project.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                Its environments and their variables go with it. This can&apos;t
                be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Keep project</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                disabled={remove.isPending}
                onClick={() => void confirm()}
              >
                {remove.isPending && <Spinner />}
                Delete project
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}
