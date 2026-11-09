// A project's Deployments tab and its loomplane.yaml setting. The tab lists
// every plan made for the project and where each service runs; a plan is
// approved or rejected on its own deployment page, never here.
import { BoxIcon, DatabaseIcon, GlobeIcon, RotateCcwIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDeploymentsList,
  useEnvironmentConnectionsChoose,
  useEnvironmentsList,
  useManifestsCurrent,
  useManifestsDraft,
  useManifestsEdit,
  useManifestsSave,
  usePlansList,
  useProjectsGraph,
  useProvidersList,
} from "@/client/forge/loomplane/hooks"
import type {
  EditInputBody,
  Finding,
  Manifest,
  Plan,
  Project,
  Provider,
} from "@/client/forge/loomplane/types"
import {
  useInvalidate,
  useMutation,
  useQuery,
  useRefetchWhile,
} from "@/client/hooks"
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
import {
  PlacementPanel,
  RunsOnCell,
  usePlacementContext,
} from "@/components/deployment-targets"
import { ManifestHistory } from "@/components/manifest-history"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone as RunTone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { lockLabel, resourceLabel } from "@/lib/architecture"
import {
  useDeploymentDraft,
  useDeploymentWrites,
} from "@/lib/deployment-writes"
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useProjectCan } from "@/lib/project-permissions"
import {
  placementRows,
  placementSettings,
  runsOn,
  type PlacementRow,
} from "@/lib/placement"
import {
  changeCount,
  deploymentStatus,
  isDeploying,
  isPlanning,
  planServices,
  planStatus,
  type Tone,
  withResource,
} from "@/lib/plans"
import { useNow } from "@/lib/use-runs"
import { Link } from "@/lib/router"

/**
 * DeployTab is a project's Deployments tab: its plans and deployments,
 * newest first, and where each service runs in each environment.
 */
export function DeployTab({ project }: { project: Project }) {
  return (
    <div className="grid gap-6">
      <DeploymentHistory project={project} />
      <WhereServicesRun project={project} />
    </div>
  )
}

// A plan's tones in the run colors: waiting is amber, done is green.
const tones: Record<Tone, RunTone> = {
  ok: "live",
  wait: "waiting",
  bad: "failed",
  quiet: "quiet",
}

const pageSize = 10

function DeploymentHistory({ project }: { project: Project }) {
  const plans = useQuery(usePlansList, { path: { id: project.id } })
  const deployments = useQuery(useDeploymentsList, {
    path: { id: project.id },
  })
  const [page, setPage] = useState(0)
  const list = [...(plans.data?.plans ?? [])].sort((a, b) =>
    b.createdAt.localeCompare(a.createdAt)
  )
  const runs = deployments.data?.deployments ?? []
  useRefetchWhile(
    list.some((p) => isPlanning(p.status)),
    plans.refetch
  )
  useRefetchWhile(
    runs.some((d) => isDeploying(d.status)),
    deployments.refetch,
    2000
  )
  const pages = Math.max(1, Math.ceil(list.length / pageSize))
  const at = Math.min(page, pages - 1)
  const shown = list.slice(at * pageSize, (at + 1) * pageSize)
  const now = useNow(60000)

  return (
    <section className="grid gap-2.5" aria-label="Deployments">
      <CloudSectionHead
        title="Deployments"
        count={plans.data ? list.length : undefined}
        description="Every plan for this project. Open one to approve it, follow it, or roll it back."
      />
      {plans.status === "error" ? (
        <QueryError
          error={plans.error}
          fallback="Deployments couldn't be loaded."
          retry={plans.refetch}
        />
      ) : !plans.data ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : list.length === 0 ? (
        <CloudEmpty
          kind="list"
          title="No deployments yet"
          action={
            <Button
              size="sm"
              variant="outline"
              nativeButton={false}
              render={
                <Link to="/deployments/new" search={{ project: project.id }} />
              }
            >
              Deploy
            </Button>
          }
        >
          Each plan you make for {project.name} shows here, with what it
          changed.
        </CloudEmpty>
      ) : (
        <>
          {deployments.status === "error" && (
            <QueryError
              error={deployments.error}
              fallback="Deployment progress couldn't be loaded."
              retry={deployments.refetch}
            />
          )}
          <CloudTable
            label="Deployments"
            columns="minmax(0,1.2fr) 150px minmax(0,1fr) 120px 80px"
            head={["Deployment", "Status", "Changes", "Started", ""]}
            footer={
              list.length > pageSize ? (
                <>
                  <span>
                    Showing {shown.length} of {list.length}
                  </span>
                  <span className="flex gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={at === 0}
                      onClick={() => setPage(at - 1)}
                    >
                      Newer
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={at >= pages - 1}
                      onClick={() => setPage(at + 1)}
                    >
                      Older
                    </Button>
                  </span>
                </>
              ) : undefined
            }
          >
            {shown.map((p) => {
              const run = runs
                .filter((d) => d.planId === p.id)
                .sort((a, b) => b.startedAt.localeCompare(a.startedAt))[0]
              const status = run
                ? deploymentStatus(run.status)
                : planStatus(p.status)
              const started = run?.startedAt ?? p.createdAt
              return (
                <CloudTableRow key={p.id}>
                  <NameCell
                    name={
                      <Link
                        to="/deployments/$id"
                        params={{ id: p.id }}
                        className="text-foreground underline-offset-2 hover:underline"
                      >
                        {planTitle(p)}
                      </Link>
                    }
                    meta={[
                      lockLabel(p.sources),
                      `loomplane.yaml revision ${p.manifestRevision}`,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  />
                  <Cell>
                    <StatusPill tone={tones[status.tone]}>
                      {status.label}
                    </StatusPill>
                  </Cell>
                  <Cell className="text-muted-foreground tabular-nums">
                    {changesLine(p)}
                  </Cell>
                  <Cell className="text-muted-foreground">
                    <time dateTime={started} title={formatDate(started)}>
                      {relative(Date.parse(started), now)}
                    </time>
                  </Cell>
                  <ActionsCell>
                    <Button
                      size="sm"
                      variant="outline"
                      nativeButton={false}
                      render={
                        <Link to="/deployments/$id" params={{ id: p.id }} />
                      }
                    >
                      Open
                    </Button>
                  </ActionsCell>
                </CloudTableRow>
              )
            })}
          </CloudTable>
        </>
      )}
    </section>
  )
}

/** planTitle names a plan by where it goes and what it moves. */
function planTitle(p: Plan): string {
  if (p.rollback) return `${p.environment} · rollback`
  const n = planServices(p).length
  return n
    ? `${p.environment} · ${n} ${n === 1 ? "service" : "services"}`
    : p.environment
}

/** changesLine says what a plan does, as short counts. */
function changesLine(p: Plan): string {
  if (isPlanning(p.status)) return "Working it out"
  if (p.status === "failed") return "None"
  if (p.problems.length > 0)
    return `${p.problems.length} ${p.problems.length === 1 ? "problem" : "problems"}`
  if (changeCount(p) === 0) return "Nothing to change"
  const s = p.summary
  return [
    s.replace && `${s.replace} replace`,
    s.create && `${s.create} create`,
    s.update && `${s.update} update`,
    s.delete && `${s.delete} delete`,
    s.keep && `${s.keep} kept`,
  ]
    .filter(Boolean)
    .join(", ")
}

const kinds: Record<string, string> = {
  "http-service": "HTTP service",
  "web-app": "Web app",
  "static-site": "Static site",
  worker: "Worker",
  job: "Job",
  cron: "Scheduled job",
}

/** kindLabel names what a placement row is. */
export function kindLabel(row: PlacementRow): string {
  if (row.section === "gateways") return "Gateway"
  if (row.section === "resources") return resourceLabel(row.kind)
  if (!row.kind) return "Service"
  return kinds[row.kind] ?? row.kind.replace(/-/g, " ")
}

/** RowGlyph is a placement row's tile icon. */
export function RowGlyph({ row }: { row: PlacementRow }) {
  if (row.section === "resources") return <DatabaseIcon />
  if (row.kind === "static-site" || row.kind === "web-app") return <GlobeIcon />
  return <BoxIcon />
}

function WhereServicesRun({ project }: { project: Project }) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const view = useQuery(useProjectsGraph, { path: { id: project.id } })
  const { onPendingChange } = useDeploymentWrites(project.id)
  const [picked, setPicked] = useState("")
  const [open, setOpen] = useState<PlacementRow | null>(null)
  const list = environments.data?.environments ?? []
  const environment = list.some((e) => e.name === picked)
    ? picked
    : (list[0]?.name ?? "")
  const ctx = usePlacementContext(
    project.id,
    list.find((e) => e.name === environment)?.id
  )
  const manifest = current.data?.manifest
  const rows =
    manifest && view.data?.graph
      ? placementRows(manifest, view.data.graph, view.data.excluded ?? [])
      : []

  const body = () => {
    if (environments.status === "error")
      return (
        <QueryError
          error={environments.error}
          fallback="Environments couldn't be loaded."
          retry={environments.refetch}
        />
      )
    if (current.status === "error" && statusOf(current.error) === 404)
      return (
        <SuggestedConfiguration
          projectId={project.id}
          onPendingChange={onPendingChange}
        />
      )
    for (const [q, what] of [
      [current, "loomplane.yaml"],
      [view, "The service graph"],
      [ctx.providers, "Targets"],
    ] as const)
      if (q.status === "error")
        return (
          <QueryError
            error={q.error}
            fallback={`${what} couldn't be loaded.`}
            retry={q.refetch}
          />
        )
    if (
      !environments.data ||
      !current.data ||
      !view.data ||
      !ctx.providers.data
    )
      return <Skeleton className="h-32 rounded-xl" />
    if (list.length === 0)
      return (
        <CloudEmpty kind="list" title="No environments yet">
          Add an environment on the Overview tab, then choose where its services
          run.
        </CloudEmpty>
      )
    if (!view.data.graph)
      return (
        <CloudEmpty kind="notice" title="Analyze your repositories first">
          Where a service runs is chosen once an analysis has found it.
        </CloudEmpty>
      )
    if (!manifest || rows.length === 0)
      return (
        <CloudEmpty kind="list" title="No services to place yet">
          Add services or resources to loomplane.yaml, then choose where they
          run.
        </CloudEmpty>
      )
    return (
      <CloudTable
        label={`Where services run in ${environment}`}
        columns="minmax(0,1fr) minmax(0,1.4fr) minmax(0,1fr) 100px"
        head={["Service", "Runs on", "Tags", ""]}
      >
        {rows.map((row) => {
          const value = placementSettings(manifest, row, environment)
          return (
            <CloudTableRow key={`${row.section}:${row.name}`}>
              <NameCell
                tile={<RowGlyph row={row} />}
                name={row.name}
                meta={kindLabel(row)}
              />
              <RunsOnCell
                runs={runsOn(
                  value,
                  environment,
                  ctx.catalog,
                  ctx.cloudTargets,
                  ctx.connections
                )}
              />
              <Cell className="flex flex-wrap gap-1">
                {(value.cloudTags ?? []).map((t) => (
                  <span
                    key={t}
                    className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[11.5px]"
                  >
                    {t}
                  </span>
                ))}
              </Cell>
              <ActionsCell>
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`Change where ${row.name} runs`}
                  onClick={() => setOpen(row)}
                >
                  Change
                </Button>
              </ActionsCell>
            </CloudTableRow>
          )
        })}
      </CloudTable>
    )
  }

  return (
    <section className="grid gap-2.5" aria-label="Where services run">
      <CloudSectionHead
        title="Where services run"
        description="Each environment's standing placement. A change saves a new revision of loomplane.yaml; nothing moves until the next deploy."
      >
        {list.length > 0 && (
          <Segments
            label="Environment"
            value={environment}
            onChange={setPicked}
            items={list.map((e) => ({ id: e.name, label: e.name }))}
          />
        )}
      </CloudSectionHead>
      {body()}
      {manifest && current.data && (
        <PlacementPanel
          projectId={project.id}
          manifest={manifest}
          revision={current.data.revision}
          row={open}
          environment={environment}
          environments={list}
          mode="save"
          onClose={() => setOpen(null)}
          onPendingChange={onPendingChange}
        />
      )}
    </section>
  )
}

/**
 * SuggestedConfiguration starts a project's loomplane.yaml from the draft
 * its latest analysis suggests.
 */
export function SuggestedConfiguration({
  projectId,
  onPendingChange,
}: {
  projectId: string
  onPendingChange: (pending: boolean) => void
}) {
  const draft = useQuery(useManifestsDraft, { path: { id: projectId } })
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const can = useProjectCan(projectId)
  const [error, setError] = useState("")
  const initialize = async () => {
    if (!draft.data) return
    setError("")
    onPendingChange(true)
    try {
      await save.mutateAsync({
        path: { id: projectId },
        body: { yaml: draft.data.yaml, baseRevision: 0 },
      })
      invalidate(useManifestsCurrent)
    } catch (err) {
      setError(messageOf(err, "The suggested configuration couldn't be saved."))
      if (statusOf(err) === 409) invalidate(useManifestsCurrent)
    } finally {
      onPendingChange(false)
    }
  }
  if (draft.status === "error")
    return (
      <QueryError
        error={draft.error}
        fallback="Suggested configuration couldn't be loaded. Analyze the repositories first."
        retry={draft.refetch}
      />
    )
  if (!draft.data) return <Skeleton className="h-32 rounded-xl" />
  return (
    <div className="grid gap-2.5">
      <CloudEmpty
        kind="notice"
        title="Start with your repository"
        action={
          <Button
            type="button"
            disabled={save.isPending || !can("project:update")}
            onClick={() => void initialize()}
          >
            {save.isPending && <Spinner />}
            Use suggested configuration
          </Button>
        }
      >
        We suggested services and infrastructure from your latest analysis. You
        can change where each one runs before planning.
      </CloudEmpty>
      {error && (
        <CloudCallout tone="error" title="The configuration wasn't saved">
          {error}
        </CloudCallout>
      )}
    </div>
  )
}

/**
 * ManifestSettings is loomplane.yaml in a project's Settings: its revision,
 * its history, and the editor, opened on request.
 */
export function ManifestSettings({ project }: { project: Project }) {
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const { onPendingChange } = useDeploymentWrites(project.id)
  const [editing, setEditing] = useState(false)
  if (current.data) {
    const { revision } = current.data
    return (
      <section className="grid gap-2.5" aria-label="loomplane.yaml">
        <CloudSectionHead
          title={
            <>
              loomplane.yaml{" "}
              <span className="font-mono text-xs font-normal text-muted-foreground tabular-nums">
                revision {revision}
              </span>
            </>
          }
          description="Where each service runs and what it needs. Every save adds a revision, and each plan names the one it was made from."
        >
          {revision > 1 && <ManifestHistory projectId={project.id} />}
          <Button
            size="sm"
            variant="outline"
            aria-expanded={editing}
            onClick={() => setEditing(!editing)}
          >
            {editing ? "Close editor" : "Edit loomplane.yaml"}
          </Button>
        </CloudSectionHead>
        {/* Hidden, not unmounted, so closing keeps an unsaved draft. */}
        <div hidden={!editing}>
          <ManifestEditor
            key={project.id}
            project={project}
            yaml={current.data.yaml}
            revision={revision}
            savedAt={current.data.createdAt}
            onPendingChange={onPendingChange}
          />
        </div>
      </section>
    )
  }
  return (
    <section className="grid gap-2.5" aria-label="loomplane.yaml">
      {current.status === "error" && statusOf(current.error) === 404 ? (
        <DraftedManifest project={project} onPendingChange={onPendingChange} />
      ) : (
        <>
          <CloudSectionHead title="loomplane.yaml" />
          {current.status === "error" ? (
            <QueryError
              error={current.error}
              fallback="loomplane.yaml couldn't be loaded."
              retry={current.refetch}
            />
          ) : (
            <Skeleton className="h-32 rounded-xl" />
          )}
        </>
      )}
    </section>
  )
}

function DraftedManifest({
  project,
  onPendingChange,
}: {
  project: Project
  onPendingChange: (pending: boolean) => void
}) {
  const draft = useQuery(useManifestsDraft, { path: { id: project.id } })
  if (draft.status === "error")
    return (
      <>
        <CloudSectionHead title="loomplane.yaml" />
        <CloudEmpty kind="notice" title="No loomplane.yaml yet">
          It says where each service runs. Analyze the repository first, and
          Loomplane drafts one from what it finds.
        </CloudEmpty>
      </>
    )
  if (!draft.data) return <Skeleton className="h-32 rounded-xl" />
  const from = lockLabel(draft.data.sources)
  return (
    <>
      <CloudSectionHead
        title={
          <span className="inline-flex items-center gap-2">
            loomplane.yaml <StatusPill tone="waiting">Draft</StatusPill>
          </span>
        }
        description={`A draft from ${from || "the latest analysis"}: every service on the first target that runs it, and every resource found with good evidence. Check it, then save it as the first revision.`}
      />
      <ManifestEditor
        project={project}
        yaml={draft.data.yaml}
        draftedFrom={from}
        onPendingChange={onPendingChange}
      />
    </>
  )
}

/**
 * ManifestEditor edits loomplane.yaml as text. A save that doesn't parse
 * comes back with every problem and where it is.
 */
export function ManifestEditor({
  project,
  yaml,
  revision,
  savedAt,
  draftedFrom,
  onPendingChange,
}: {
  project: Project
  yaml: string
  revision?: number
  savedAt?: string
  /** draftedFrom names the commits a draft was made from; set for a draft. */
  draftedFrom?: string
  onPendingChange?: (pending: boolean) => void
}) {
  const can = useProjectCan(project.id)
  const mayEdit = can("project:update")
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const [text, setText] = useState(yaml)
  const [lastYAML, setLastYAML] = useState(yaml)
  const [conflict, setConflict] = useState(false)
  const [problems, setProblems] = useState<string[]>([])
  // A placement save can refresh this editor while it has local changes.
  // Adopt the new YAML only when there's no draft to preserve.
  if (lastYAML !== yaml) {
    setLastYAML(yaml)
    if (text === lastYAML || text === yaml) {
      setText(yaml)
      setConflict(false)
    } else {
      setConflict(true)
    }
  }
  const dirty = text !== yaml
  useDeploymentDraft(dirty, onPendingChange)
  const drafted = draftedFrom !== undefined

  const submit = async () => {
    setProblems([])
    onPendingChange?.(true)
    try {
      const rev = await save.mutateAsync({
        path: { id: project.id },
        body: { yaml: text, baseRevision: revision ?? 0 },
      })
      invalidate(useManifestsCurrent)
      toast.success(
        rev.revision === revision
          ? "Nothing changed, so no new revision"
          : `Saved revision ${rev.revision}`
      )
    } catch (err) {
      if (statusOf(err) === 409) invalidate(useManifestsCurrent)
      setProblems(
        manifestProblems(messageOf(err, "The manifest wasn't saved."))
      )
    } finally {
      onPendingChange?.(false)
    }
  }

  return (
    <div className="grid gap-2.5">
      {conflict && (
        <div role="alert">
          <CloudCallout title="A newer revision was saved">
            A newer manifest revision was saved. Your unsaved YAML is preserved.
            Copy your changes, then undo to load the latest revision before
            saving.
          </CloudCallout>
        </div>
      )}
      <div className="overflow-hidden rounded-xl bg-card ring-1 ring-border">
        <Textarea
          aria-label="Manifest"
          value={text}
          onChange={(e) => setText(e.target.value)}
          readOnly={!mayEdit}
          spellCheck={false}
          aria-invalid={problems.length > 0}
          className="max-h-[32rem] min-h-64 rounded-none border-0 bg-transparent font-mono text-xs leading-relaxed shadow-none focus-visible:ring-0 md:text-xs"
        />
        {mayEdit && (
          <div className="flex flex-wrap items-center justify-between gap-2 border-t bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
            <span>
              {savedAt && (
                <>
                  Saved <time dateTime={savedAt}>{formatDate(savedAt)}</time>
                </>
              )}
            </span>
            <span className="flex gap-2">
              {dirty && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setText(yaml)
                    setConflict(false)
                    setProblems([])
                  }}
                >
                  <RotateCcwIcon />
                  Undo changes
                </Button>
              )}
              <Button
                size="sm"
                disabled={(!dirty && !drafted) || save.isPending || conflict}
                onClick={() => void submit()}
              >
                {save.isPending && <Spinner />}
                {drafted ? "Save manifest" : "Save revision"}
              </Button>
            </span>
          </div>
        )}
      </div>
      {problems.length > 0 && (
        <CloudCallout tone="error" title="The manifest wasn't saved">
          <ul className="grid gap-1">
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </CloudCallout>
      )}
    </div>
  )
}

/**
 * manifestProblems splits the API's "invalid manifest: a; b" into one
 * line per problem.
 */
function manifestProblems(message: string): string[] {
  const body = message.replace(/^Invalid manifest:\s*/i, "")
  if (body === message) return [message]
  return body
    .replace(/\.$/, "")
    .split("; ")
    .map((p) => p.charAt(0).toUpperCase() + p.slice(1))
}

// Findings about which cloud connection to deploy with carry the cloud
// in their path, connections.<cloud>, which isn't a manifest path.
const connectionPath = "connections."

/**
 * FindingActions are the fixes a plan's finding offers, for the deployment
 * page to show beside it: connect a cloud, pick the account an environment
 * deploys it with, or add or ignore a resource the manifest doesn't
 * provision. Each saves a setting; planning again shows the result.
 */
export function FindingActions({
  finding: f,
  project,
  environment,
}: {
  finding: Finding
  project: Project
  environment: string
}) {
  const { can: workspaceCan } = useOrganization()
  const can = useProjectCan(project.id)
  if (f.code === "no-connection")
    return (
      <Link
        to="/integrations"
        className="text-[13px] underline-offset-4 hover:underline"
      >
        Connect one under Integrations
      </Link>
    )
  if (
    workspaceCan("cloud_account:manage") &&
    f.path?.startsWith(connectionPath)
  )
    return (
      <ConnectionPicker
        project={project}
        environment={environment}
        cloud={f.path.slice(connectionPath.length)}
      />
    )
  if (
    can("project:update") &&
    f.name &&
    (f.code === "unconfirmed-resource" || f.code === "unprovided-resource")
  )
    return <ResourceChoice project={project} kind={f.name} />
  return null
}

/**
 * ConnectionPicker chooses which of the organization's ready connections
 * an environment deploys to a cloud with. Planning again uses it.
 */
function ConnectionPicker({
  project,
  environment,
  cloud,
}: {
  project: Project
  environment: string
  cloud: string
}) {
  const list = useQuery(useConnectionsList)
  const choose = useMutation(useEnvironmentConnectionsChoose)
  const ready =
    list.data?.connections.filter(
      (c) => c.cloud === cloud && c.status === "ready"
    ) ?? []
  if (ready.length === 0) return null
  const pick = async (id: string | null) => {
    if (!id) return
    try {
      await choose.mutateAsync({
        path: { id: project.id, environment, cloud },
        body: { connectionId: id },
      })
      const name = ready.find((c) => c.id === id)?.name ?? "it"
      toast.success(
        `${environment} deploys to ${cloudLabel(cloud)} with ${name}. Plan again to use it.`
      )
    } catch (err) {
      toast.error(messageOf(err, "The connection wasn't chosen."))
    }
  }
  return (
    <Select onValueChange={(v) => void pick(typeof v === "string" ? v : null)}>
      <SelectTrigger
        size="sm"
        className="w-fit min-w-48"
        aria-label={`Connection for ${cloudLabel(cloud)}`}
        disabled={choose.isPending}
      >
        <SelectValue placeholder={`Choose a ${cloudLabel(cloud)} connection`} />
      </SelectTrigger>
      <SelectContent>
        {ready.map((c) => (
          <SelectItem key={c.id} value={c.id}>
            {c.name} · {c.account}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/**
 * ResourceChoice answers a finding about a resource the manifest doesn't
 * provision: add one, or ignore the kind. Either saves a new revision.
 */
function ResourceChoice({ project, kind }: { project: Project; kind: string }) {
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const providers = useQuery(useProvidersList)
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const manifest = current.data?.manifest
  const catalog = providers.data?.providers
  if (!manifest || !catalog) return null
  // Adding a resource nothing provisions would only block the next plan.
  const provisionable = catalog.some((p) =>
    p.provisions?.some((v) => v.kind === kind)
  )
  // The edit is made to the saved YAML, on the server, so its comments
  // survive.
  const apply = async (body: EditInputBody, done: string) => {
    try {
      await save.mutateAsync({ path: { id: project.id }, body })
      invalidate(useManifestsCurrent)
      toast.success(`${done}. Plan again to see the result.`)
    } catch (err) {
      toast.error(messageOf(err, "The manifest wasn't saved."))
    }
  }
  return (
    <span className="flex flex-wrap gap-2">
      {provisionable && (
        <Button
          size="sm"
          variant="outline"
          disabled={save.isPending}
          onClick={() =>
            void apply(
              { addResource: added(manifest, kind, catalog) },
              `Added ${resourceLabel(kind)} to the manifest`
            )
          }
        >
          Add {resourceLabel(kind)}
        </Button>
      )}
      <Button
        size="sm"
        variant="outline"
        disabled={save.isPending}
        onClick={() =>
          void apply(
            { ignore: kind },
            `The manifest ignores ${resourceLabel(kind)} now`
          )
        }
      >
        It doesn&apos;t use {resourceLabel(kind)}
      </Button>
    </span>
  )
}

/** added is the resource withResource would add: its name, kind and provider. */
function added(m: Manifest, kind: string, catalog: Provider[]) {
  const next = withResource(m, kind, catalog)
  const name =
    Object.keys(next.resources ?? {}).find(
      (n) => !(n in (m.resources ?? {}))
    ) ?? kind
  const r = (next.resources ?? {})[name] as { provider?: string }
  return { name, kind, provider: r?.provider }
}
