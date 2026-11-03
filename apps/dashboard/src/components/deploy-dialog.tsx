// The deploy screen: pick an environment and the services that go out, and
// Loomplane plans the change. A deployment always starts as a plan, so
// nothing touches a cloud until someone approves it on the deployment page.
// Placement changed here waits until "Review plan" saves it with the plan.
import { GitBranchIcon } from "lucide-react"
import { useState, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useBindingsList,
  useDeploymentsList,
  useEnvironmentsList,
  useManifestsCurrent,
  usePlansCreate,
  usePlansList,
  useProjectsGraph,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  CreatePlanInputBody,
  Environment,
  Project,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudPageHeader,
  CloudTable,
  CloudTableRow,
  NameCell,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import {
  kindLabel,
  ManifestEditor,
  SuggestedConfiguration,
} from "@/components/deploy"
import {
  PlacementPanel,
  RunsOnCell,
  usePlacementContext,
  usePlacementSave,
  type PendingPlacement,
} from "@/components/deployment-targets"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { DropdownMenuItem } from "@/components/ui/dropdown-menu"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { shortSHA } from "@/lib/architecture"
import { useDeploymentScope } from "@/lib/deployment-scope"
import { useDeploymentWrites } from "@/lib/deployment-writes"
import { relative, repoLabel } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import {
  placementRows,
  placementSettings,
  rowKey,
  runsOn,
  sinceLastDeploy,
  type PlacementRow,
} from "@/lib/placement"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { useNow } from "@/lib/use-runs"
import { Link, useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

/** DeployPreset is what a link to the deploy screen already chose. */
export type DeployPreset = {
  project?: string
  environment?: string
  /** service preselects only this service. */
  service?: string
}

/**
 * DeploymentForm is the deploy screen. done leaves it without planning.
 */
export function DeploymentForm({
  preset,
  done,
}: {
  preset: DeployPreset
  done: () => void
}) {
  const list = useQuery(useProjectsList)
  const navigate = useNavigate()
  const projects = list.data?.projects ?? []
  const [chosen, setChosen] = useState(preset.project ?? "")
  const [picking, setPicking] = useState(!preset.project)
  const project = chosen ? projects.find((p) => p.id === chosen) : projects[0]

  if (list.status === "error")
    return (
      <QueryError
        error={list.error}
        fallback="Projects couldn't be loaded."
        retry={list.refetch}
      />
    )
  if (!list.data) return <Skeleton className="h-40 rounded-xl" />
  if (projects.length === 0)
    return (
      <CloudEmpty
        kind="notice"
        title="No projects to deploy"
        action={
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              done()
              void navigate({ to: "/projects" })
            }}
          >
            Open projects
          </Button>
        }
      >
        Create a project and add its repositories first.
      </CloudEmpty>
    )
  if (!project)
    return (
      <CloudEmpty
        kind="notice"
        title="Project unavailable"
        action={
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              setChosen("")
              setPicking(true)
            }}
          >
            Choose another project
          </Button>
        }
      >
        This project may have been removed, or you may not have access.
      </CloudEmpty>
    )
  const picker = picking ? (
    <ProjectPicker projects={projects} project={project} onChange={setChosen} />
  ) : null
  return (
    // Keyed by project: its environments, services and repositories differ.
    <DeployProject
      key={project.id}
      project={project}
      preset={project.id === preset.project ? preset : {}}
      picker={picker}
      onAnother={() => setPicking(true)}
      done={done}
    />
  )
}

function ProjectPicker({
  projects,
  project,
  onChange,
}: {
  projects: Project[]
  project: Project
  onChange: (id: string) => void
}) {
  const { busy } = useDeploymentWrites(project.id)
  return (
    <section className="grid gap-2.5" aria-label="Project">
      <CloudSectionHead title="Project" description="What this deploy ships." />
      <Select
        items={projects.map((p) => ({ label: p.name, value: p.id }))}
        value={project.id}
        onValueChange={(v) => onChange(String(v))}
      >
        <SelectTrigger
          className="h-11 w-full max-w-md rounded-xl bg-card"
          aria-label="Project"
          disabled={busy}
        >
          <SelectValue>
            {(v: string) => {
              const p = projects.find((x) => x.id === v)
              if (!p) return v
              const s = projectStyle(p)
              return (
                <span className="flex items-center gap-2">
                  <ProjectIcon icon={s.icon} color={s.color} size="xs" />
                  {p.name}
                </span>
              )
            }}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {projects.map((p) => {
            const s = projectStyle(p)
            return (
              <SelectItem key={p.id} value={p.id}>
                <ProjectIcon icon={s.icon} color={s.color} size="xs" />
                {p.name}
                <span className="truncate text-muted-foreground">
                  {p.description}
                </span>
              </SelectItem>
            )
          })}
        </SelectContent>
      </Select>
    </section>
  )
}

/** DeployHeader is the screen's header; its menu fills in once it can. */
function DeployHeader({
  project,
  onAnother,
  onManifest,
  request,
}: {
  project: Project
  onAnother: () => void
  onManifest?: () => void
  /** request is the plan this screen would create, as an API call. */
  request?: string
}) {
  const s = projectStyle(project)
  return (
    <CloudPageHeader
      glyph={<ProjectIcon icon={s.icon} color={s.color} size="lg" />}
      title={`Deploy ${project.name}`}
      meta="Loomplane plans first. Nothing touches a cloud until you approve the plan."
      actions={
        <PageMenu label="More deploy options">
          <DropdownMenuItem onClick={onAnother}>
            Deploy another project
          </DropdownMenuItem>
          {onManifest && (
            <DropdownMenuItem onClick={onManifest}>
              View loomplane.yaml
            </DropdownMenuItem>
          )}
          {request && (
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard
                  ?.writeText(request)
                  .then(() => toast.success("Copied the API request"))
              }}
            >
              Copy as API request
            </DropdownMenuItem>
          )}
        </PageMenu>
      }
    />
  )
}

function DeployProject({
  project,
  preset,
  picker,
  onAnother,
  done,
}: {
  project: Project
  preset: DeployPreset
  picker: ReactNode
  onAnother: () => void
  done: () => void
}) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  const [ignorePreset, setIgnorePreset] = useState(false)
  const list = environments.data?.environments ?? []
  const shell = (body: ReactNode) => (
    <div className="grid gap-6">
      <DeployHeader project={project} onAnother={onAnother} />
      {picker}
      {body}
    </div>
  )
  if (environments.status === "error")
    return shell(
      <QueryError
        error={environments.error}
        fallback="Environments couldn't be loaded."
        retry={environments.refetch}
      />
    )
  if (!environments.data) return shell(<Skeleton className="h-32 rounded-xl" />)
  if (list.length === 0)
    return shell(
      <CloudEmpty kind="notice" title="No environments yet">
        {project.name} is still being set up. Its environments appear in a
        moment.
      </CloudEmpty>
    )
  if (
    !ignorePreset &&
    preset.environment &&
    !list.some((e) => e.name === preset.environment)
  )
    return shell(
      <CloudEmpty
        kind="notice"
        title="Environment unavailable"
        action={
          <Button
            type="button"
            variant="outline"
            onClick={() => setIgnorePreset(true)}
          >
            Choose another environment
          </Button>
        }
      >
        The environment you picked no longer exists. Choose another one before
        deploying.
      </CloudEmpty>
    )
  return (
    <DeployScreen
      project={project}
      environments={list}
      initial={
        list.some((e) => e.name === preset.environment)
          ? (preset.environment as string)
          : (list[0] as Environment).name
      }
      service={preset.service}
      picker={picker}
      onAnother={onAnother}
      done={done}
    />
  )
}

type Pending = Record<string, Record<string, PendingPlacement>>

function DeployScreen({
  project,
  environments,
  initial,
  service,
  picker,
  onAnother,
  done,
}: {
  project: Project
  environments: Environment[]
  initial: string
  service?: string
  picker: ReactNode
  onAnother: () => void
  done: () => void
}) {
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const create = useMutation(usePlansCreate)
  const placement = usePlacementSave(project.id)
  const { can } = useOrganization()
  const canDeploy = can("deployment:create")
  const { busy, onPendingChange } = useDeploymentWrites(project.id)
  const [environment, setEnvironmentState] = useState(initial)
  // null picks the default: what changed, or the one service asked for.
  const [picks, setPicks] = useState<string[] | null>(null)
  const [pending, setPending] = useState<Pending>({})
  const [open, setOpen] = useState<PlacementRow | null>(null)
  const [showManifest, setShowManifest] = useState(false)
  const [error, setError] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const setEnvironment = (name: string) => {
    setEnvironmentState(name)
    setPicks(null)
  }

  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const view = useQuery(useProjectsGraph, { path: { id: project.id } })
  const bindings = useQuery(useBindingsList, {
    path: { id: project.id },
    query: { environment },
  })
  const scope = useDeploymentScope(project.id, environment)
  const ctx = usePlacementContext(
    project.id,
    environments.find((e) => e.name === environment)?.id
  )
  const manifest = current.data?.manifest
  const graph = view.data?.graph
  const rows =
    manifest && graph
      ? placementRows(manifest, graph, view.data?.excluded ?? []).filter(
          (r) => r.section === "services"
        )
      : []
  const excludedCount = view.data?.excluded?.length ?? 0
  const here = pending[environment] ?? {}
  const moves = Object.keys(here).length
  const services = rows.map((row) => {
    const settings = placementSettings(manifest!, row, environment)
    const change = here[rowKey(row)]
    const value = change ? { ...settings, ...change.changes } : settings
    const binding = (bindings.data?.bindings ?? []).find(
      (b) => b.subject === "service" && b.name === row.name
    )
    const runs = (v: typeof settings) =>
      runsOn(v, environment, ctx.catalog, ctx.cloudTargets, ctx.connections)
    return {
      row,
      runs: runs(settings),
      moves: change ? runs(value) : undefined,
      since: sinceLastDeploy(row, graph, binding, value.enabled),
    }
  })
  const names = rows.map((r) => r.name)
  const changed = services.filter((s) => s.since.changed).map((s) => s.row.name)
  // Without what runs there, nothing can say what changed: offer them all.
  const known = !!bindings.data || bindings.status === "error"
  const fallback =
    service && names.includes(service)
      ? [service]
      : bindings.data
        ? changed
        : known
          ? names
          : []
  const picked = (picks ?? fallback).filter((n) => names.includes(n))
  const toggle = (name: string) =>
    setPicks(
      picked.includes(name)
        ? picked.filter((n) => n !== name)
        : [...picked, name]
    )

  const body: CreatePlanInputBody = {
    environment,
    ...scope.body,
    ...(picked.length < names.length ? { services: picked } : {}),
  }
  const request = `curl -X POST "${window.location.origin}/api/v1/projects/${project.id}/plans" -H "Authorization: Bearer $LOOMPLANE_TOKEN" -H "Content-Type: application/json" -d '${JSON.stringify(body)}'`
  const working = submitting || create.isPending || placement.pending
  const blocked =
    working ||
    busy ||
    !canDeploy ||
    !manifest ||
    !known ||
    !scope.ready ||
    picked.length === 0

  const review = async () => {
    if (blocked || !current.data?.manifest) return
    const edits = Object.entries(here).flatMap(([key, change]) => {
      const row = rows.find((r) => rowKey(r) === key)
      return row ? [{ ...change, row, environment }] : []
    })
    setError("")
    setSubmitting(true)
    onPendingChange(true)
    try {
      if (edits.length) {
        await placement.run(
          edits,
          { manifest: current.data.manifest, revision: current.data.revision },
          environments
        )
        // Saved: a retry after a failed plan mustn't save them twice.
        setPending((p) => ({ ...p, [environment]: {} }))
      }
      const p = await create.mutateAsync({ path: { id: project.id }, body })
      invalidate(usePlansList)
      void navigate({ to: "/deployments/$id", params: { id: p.id } })
      toast.success(`Planning ${project.name} to ${environment}`, {
        description: "Review the plan, then approve it to deploy.",
      })
    } catch (err) {
      setError(
        messageOf(
          err,
          edits.length
            ? "The placement or the plan couldn't be saved."
            : "The plan couldn't start."
        )
      )
    } finally {
      setSubmitting(false)
      onPendingChange(false)
    }
  }

  const note = busy
    ? "Save or undo your loomplane.yaml changes to continue."
    : !manifest
      ? "Save a loomplane.yaml first."
      : picked.length === 0
        ? "Pick at least one service to deploy."
        : moves
          ? "Reviewing saves the placement change to loomplane.yaml, then plans. You approve on the next page."
          : "Planning makes no cloud changes. You approve on the next page."

  return (
    <div className="grid gap-6">
      <DeployHeader
        project={project}
        onAnother={onAnother}
        onManifest={current.data ? () => setShowManifest(true) : undefined}
        request={request}
      />
      {picker}
      {current.data && (
        <section
          className="grid gap-2.5"
          aria-label="loomplane.yaml"
          hidden={!showManifest}
        >
          <CloudSectionHead
            title={
              <>
                loomplane.yaml{" "}
                <span className="font-mono text-xs font-normal text-muted-foreground tabular-nums">
                  revision {current.data.revision}
                </span>
              </>
            }
            description="Where each service runs and what it needs. Saving it adds a revision."
          >
            <Button
              size="sm"
              variant="outline"
              onClick={() => setShowManifest(false)}
            >
              Close
            </Button>
          </CloudSectionHead>
          <ManifestEditor
            project={project}
            yaml={current.data.yaml}
            revision={current.data.revision}
            savedAt={current.data.createdAt}
            onPendingChange={onPendingChange}
          />
        </section>
      )}

      <section className="grid gap-2.5" aria-label="Environment">
        <CloudSectionHead
          title="Environment"
          description="Where this deploy goes."
        />
        <EnvironmentChoice
          project={project}
          environments={environments}
          value={environment}
          disabled={busy || working}
          onChange={setEnvironment}
        />
      </section>

      <section className="grid gap-2.5" aria-label="Services">
        <CloudSectionHead
          title={
            <>
              Services{" "}
              {rows.length > 0 && (
                <span className="font-mono text-xs font-normal text-muted-foreground tabular-nums">
                  {picked.length} of {rows.length}
                </span>
              )}
            </>
          }
          description={`Services with changes since the last deploy to ${environment} are ticked. "Change" sets where one runs.`}
        >
          {rows.length > 0 && (
            <>
              <Button
                size="sm"
                variant="outline"
                disabled={working}
                onClick={() => setPicks(changed)}
              >
                Only changed
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={working}
                onClick={() => setPicks(names)}
              >
                All
              </Button>
            </>
          )}
        </CloudSectionHead>
        {current.status === "error" && statusOf(current.error) === 404 ? (
          <SuggestedConfiguration
            projectId={project.id}
            onPendingChange={onPendingChange}
          />
        ) : current.status === "error" ? (
          <QueryError
            error={current.error}
            fallback="loomplane.yaml couldn't be loaded."
            retry={current.refetch}
          />
        ) : view.status === "error" ? (
          <QueryError
            error={view.error}
            fallback="The service graph couldn't be loaded."
            retry={view.refetch}
          />
        ) : !current.data || !view.data ? (
          <Skeleton className="h-32 rounded-xl" />
        ) : !graph ? (
          <CloudEmpty kind="notice" title="Analyze your repositories first">
            A deploy picks from the services an analysis found.
          </CloudEmpty>
        ) : rows.length === 0 ? (
          <CloudEmpty kind="list" title="No services to deploy yet">
            Add services to loomplane.yaml, then deploy them here.
          </CloudEmpty>
        ) : (
          <>
            {bindings.status === "error" && (
              <QueryError
                error={bindings.error}
                fallback={`What runs in ${environment} couldn't be loaded.`}
                retry={bindings.refetch}
              />
            )}
            {ctx.providers.status === "error" && (
              <QueryError
                error={ctx.providers.error}
                fallback="Targets couldn't be loaded."
                retry={ctx.providers.refetch}
              />
            )}
            <CloudTable
              label={`Services to deploy to ${environment}`}
              columns="minmax(0,1.2fr) minmax(0,1.5fr) minmax(0,1fr) 90px"
              head={["Service", "Runs on", "Since last deploy", ""]}
            >
              {services.map(({ row, runs, moves: to, since }) => (
                <CloudTableRow key={row.name}>
                  <Cell className="flex min-w-0 items-center gap-3">
                    <Checkbox
                      aria-label={`Deploy ${row.name}`}
                      checked={picked.includes(row.name)}
                      disabled={working}
                      onCheckedChange={() => toggle(row.name)}
                    />
                    <span className="grid min-w-0 gap-px">
                      <span className="truncate font-semibold">{row.name}</span>
                      <span className="truncate text-xs text-muted-foreground">
                        {kindLabel(row)}
                      </span>
                    </span>
                  </Cell>
                  <RunsOnCell runs={runs} moves={to} />
                  <Cell>
                    {bindings.data ? (
                      <StatusPill tone={since.tone}>{since.label}</StatusPill>
                    ) : bindings.status === "error" ? (
                      <span className="text-muted-foreground">Unknown</span>
                    ) : (
                      <Skeleton className="h-5 w-20 rounded-full" />
                    )}
                  </Cell>
                  <ActionsCell>
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Change where ${row.name} runs`}
                      disabled={working}
                      onClick={() => setOpen(row)}
                    >
                      Change
                    </Button>
                  </ActionsCell>
                </CloudTableRow>
              ))}
            </CloudTable>
          </>
        )}
        {excludedCount > 0 && (
          <p className="text-xs text-muted-foreground">
            {excludedCount === 1
              ? `1 service is excluded from ${project.name} and never deploys.`
              : `${excludedCount} services are excluded from ${project.name} and never deploy.`}{" "}
            <Link
              to="/projects/$id"
              params={{ id: project.id }}
              search={{ tab: "services" }}
              className="text-foreground underline underline-offset-4"
            >
              Manage exclusions
            </Link>
          </p>
        )}
      </section>

      {scope.aliases.length > 1 && (
        <Code project={project} environment={environment} scope={scope} />
      )}
      {scope.sources.status === "error" && (
        <QueryError
          error={scope.sources.error}
          fallback="Repositories couldn't be loaded."
          retry={scope.sources.refetch}
        />
      )}
      {scope.deployed.status === "error" && (
        <QueryError
          error={scope.deployed.error}
          fallback="Deployed revisions couldn't be loaded."
          retry={scope.deployed.refetch}
        />
      )}
      {error && (
        <CloudCallout tone="error" title="Nothing was planned">
          {error}
        </CloudCallout>
      )}

      <div
        className="sticky bottom-0 z-10 flex flex-wrap items-center justify-between gap-3 rounded-xl bg-card px-4 py-3 shadow-sm ring-1 ring-border"
        aria-label="Deploy summary"
        role="region"
      >
        <span className="grid min-w-0 gap-0.5">
          <span className="text-[13px] font-semibold">
            {picked.length} {picked.length === 1 ? "service" : "services"} to{" "}
            {environment}
            {moves > 0 &&
              ` · ${moves} placement ${moves === 1 ? "change" : "changes"}`}
          </span>
          <span role="status" className="text-xs text-muted-foreground">
            {note}
          </span>
        </span>
        <span className="flex gap-2">
          <Button type="button" variant="outline" onClick={done}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={blocked}
            onClick={() => void review()}
          >
            {working && <Spinner />}
            Review plan
          </Button>
        </span>
      </div>

      {manifest && current.data && (
        <PlacementPanel
          projectId={project.id}
          manifest={manifest}
          revision={current.data.revision}
          row={open}
          environment={environment}
          environments={environments}
          mode="pending"
          pending={open ? here[rowKey(open)] : undefined}
          onUse={(change) => {
            if (!open) return
            setPending((p) => ({
              ...p,
              [environment]: { ...p[environment], [rowKey(open)]: change },
            }))
            // A service whose placement changes goes out with this deploy.
            if (!picked.includes(open.name)) setPicks([...picked, open.name])
          }}
          onDiscard={
            open && here[rowKey(open)]
              ? () => {
                  const key = rowKey(open)
                  setPending((p) => {
                    const next = { ...p[environment] }
                    delete next[key]
                    return { ...p, [environment]: next }
                  })
                }
              : undefined
          }
          onClose={() => setOpen(null)}
        />
      )}
    </div>
  )
}

/**
 * EnvironmentChoice picks the environment, each card saying what runs there
 * now.
 */
function EnvironmentChoice({
  project,
  environments,
  value,
  disabled,
  onChange,
}: {
  project: Project
  environments: Environment[]
  value: string
  disabled: boolean
  onChange: (name: string) => void
}) {
  const plans = useQuery(usePlansList, { path: { id: project.id } })
  const deployments = useQuery(useDeploymentsList, {
    path: { id: project.id },
  })
  const now = useNow(60000)
  const stateOf = (name: string): { label: string; tone: Tone } => {
    if (
      (plans.data?.plans ?? []).some(
        (p) => p.environment === name && p.status === "ready"
      )
    )
      return { label: "A plan waits for you", tone: "waiting" }
    const last = (deployments.data?.deployments ?? [])
      .filter((d) => d.environment === name)
      .sort((a, b) => b.startedAt.localeCompare(a.startedAt))[0]
    if (!last) return { label: "Not deployed yet", tone: "quiet" }
    const when = relative(Date.parse(last.finishedAt ?? last.startedAt), now)
    if (last.status === "running") return { label: "Deploying", tone: "flight" }
    if (last.status === "failed")
      return { label: `Last deploy failed ${when}`, tone: "failed" }
    if (last.status === "succeeded")
      return { label: `Live, deployed ${when}`, tone: "live" }
    return { label: `Last deployed ${when}`, tone: "quiet" }
  }
  return (
    <div
      role="radiogroup"
      aria-label="Environment"
      className="grid gap-2.5 sm:grid-cols-[repeat(auto-fill,minmax(200px,1fr))]"
    >
      {environments.map((e) => {
        const on = e.name === value
        const state = stateOf(e.name)
        return (
          <button
            key={e.id}
            type="button"
            role="radio"
            aria-checked={on}
            disabled={disabled}
            onClick={() => onChange(e.name)}
            className={cn(
              "grid gap-1.5 rounded-xl bg-card px-3.5 py-3 text-left ring-1 ring-border transition-shadow focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-60",
              on && "ring-2 ring-primary"
            )}
          >
            <span className="flex items-center justify-between gap-2">
              <span className="truncate text-[13px] font-semibold">
                {e.name}
              </span>
              <span
                aria-hidden="true"
                className={cn(
                  "size-3.5 shrink-0 rounded-full border-[1.5px] border-muted-foreground/50",
                  on && "border-4 border-primary"
                )}
              />
            </span>
            {plans.data && deployments.data ? (
              <StatusPill tone={state.tone}>{state.label}</StatusPill>
            ) : (
              <Skeleton className="h-5 w-28 rounded-full" />
            )}
          </button>
        )
      })}
    </div>
  )
}

/**
 * Code says which commit each repository deploys from. Holding one keeps
 * the environment on what it runs now; one with nothing deployed there
 * can't be held.
 */
function Code({
  project,
  environment,
  scope,
}: {
  project: Project
  environment: string
  scope: ReturnType<typeof useDeploymentScope>
}) {
  const view = useQuery(useProjectsGraph, { path: { id: project.id } })
  const sources = (scope.sources.data?.sources ?? []).filter(
    (s) => !s.removedAt
  )
  return (
    <section className="grid gap-2.5" aria-label="Code">
      <CloudSectionHead
        title="Code"
        description={`The commit each repository deploys from. Holding one keeps ${environment} on what it runs now.`}
      />
      <CloudTable
        label="Repositories"
        columns="minmax(0,1.2fr) minmax(0,1fr) auto"
        head={["Repository", "Deploys from", ""]}
      >
        {sources.map((s) => {
          const latest = view.data?.graph?.sources?.find(
            (x) => x.alias === s.alias
          )?.commitSha
          const running = (scope.deployed.data?.sources ?? []).find(
            (d) => d.alias === s.alias
          )?.commitSha
          const locked = scope.locked.has(s.alias)
          const moving = scope.chosen.includes(s.alias)
          const note = locked
            ? scope.known
              ? `Nothing from it runs in ${environment} yet, so it deploys`
              : `Deploys until Loomplane knows what runs in ${environment}`
            : running && running === latest
              ? `${environment} runs this commit`
              : running
                ? `${environment} runs ${shortSHA(running)}`
                : ""
          return (
            <CloudTableRow key={s.id}>
              <NameCell
                tile={<GitBranchIcon />}
                name={s.alias}
                meta={[repoLabel(s.repository), note]
                  .filter(Boolean)
                  .join(" · ")}
              />
              <Cell className="font-mono text-xs">
                {s.ref || "default branch"}
                {latest && ` @ ${shortSHA(latest)}`}
              </Cell>
              <Cell>
                <div
                  role="radiogroup"
                  aria-label={`What ${s.alias} deploys`}
                  className="inline-flex gap-0.5 rounded-[10px] bg-muted p-[3px]"
                >
                  {(
                    [
                      ["move", "Deploy this commit"],
                      ["hold", "Hold"],
                    ] as const
                  ).map(([mode, label]) => {
                    const on = mode === "move" ? moving : !moving
                    const stuck = mode === "hold" && locked
                    return (
                      <button
                        key={mode}
                        type="button"
                        role="radio"
                        aria-checked={on}
                        aria-disabled={stuck || undefined}
                        title={stuck ? note : undefined}
                        onClick={() => {
                          if (!on && !stuck) scope.toggle(s.alias)
                        }}
                        className={cn(
                          "flex h-7 items-center rounded-lg px-2.5 text-[12.5px] font-medium whitespace-nowrap focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
                          on
                            ? "bg-background text-foreground shadow-xs"
                            : "text-muted-foreground hover:text-foreground",
                          stuck && "cursor-not-allowed opacity-60"
                        )}
                      >
                        {label}
                      </button>
                    )
                  })}
                </div>
              </Cell>
            </CloudTableRow>
          )
        })}
      </CloudTable>
    </section>
  )
}
