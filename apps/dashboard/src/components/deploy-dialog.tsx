// New deployment: pick a project and an environment, and Loomplane plans
// the change. A deployment always starts as a plan, so nothing touches a
// cloud until someone approves what the plan says it will do.
import { ArrowRightIcon, FileCodeIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import { DeploymentTargets } from "@/components/deployment-targets"
import { ManifestEditor } from "@/components/deploy"
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@/components/ui/card"
import {
  useEnvironmentsList,
  usePlansCreate,
  useManifestsCurrent,
  useManifestsDraft,
  useManifestsSave,
  usePlansList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { segmented } from "@/components/runs"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
} from "@/components/ui/empty"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { placementSummary } from "@/lib/placement"
import { useDeploymentScope } from "@/lib/deployment-scope"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"
import { useOrganization } from "@/lib/organization"
import { useDeploymentWrites } from "@/lib/deployment-writes"

export type DeployPreset = { project?: string; environment?: string }

export function DeployDialog({
  preset,
  onOpenChange,
}: {
  preset: DeployPreset | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={preset !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {/* Keyed so each opening starts from its preset. */}
        {preset !== null && (
          <DeploymentForm
            key={JSON.stringify(preset)}
            preset={preset}
            done={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

export function DeploymentForm({
  preset,
  done,
  page = false,
}: {
  preset: DeployPreset
  done: () => void
  page?: boolean
}) {
  const list = useQuery(useProjectsList)
  const navigate = useNavigate()
  const projects = list.data?.projects ?? []
  const [chosen, setChosen] = useState(preset.project ?? "")
  const project = chosen ? projects.find((p) => p.id === chosen) : projects[0]
  const { busy } = useDeploymentWrites(project?.id ?? "")

  return (
    <div className="grid gap-5">
      {!page && (
        <DialogHeader>
          <DialogTitle>New deployment</DialogTitle>
          <DialogDescription>
            Loomplane plans the change first, so you see what it will touch
            before anything runs. Nothing changes in a cloud until the plan is
            approved.
          </DialogDescription>
        </DialogHeader>
      )}
      {list.status === "error" ? (
        <QueryError
          error={list.error}
          fallback="Projects couldn't be loaded."
          retry={list.refetch}
        />
      ) : !list.data ? (
        <Skeleton className="h-40 w-full rounded-xl" />
      ) : projects.length === 0 ? (
        <Empty preview={false}>
          <EmptyHeader>
            <EmptyTitle>No projects to deploy</EmptyTitle>
            <EmptyDescription>
              Create a project and add its repositories first.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button
              type="button"
              onClick={() => {
                done()
                void navigate({ to: "/projects" })
              }}
            >
              Open projects
            </Button>
          </EmptyContent>
        </Empty>
      ) : project ? (
        <FieldGroup>
          <Field>
            <FieldLabel>
              <StepNumber n={1} />
              Project
            </FieldLabel>
            <Select
              items={projects.map((p) => ({ label: p.name, value: p.id }))}
              value={project.id}
              onValueChange={(v) => setChosen(String(v))}
            >
              <SelectTrigger
                className="h-11 w-full rounded-xl bg-card"
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
          </Field>
          {/* Keyed by project: its environments and repositories differ. */}
          <ProjectChoices
            key={project.id}
            project={project}
            preset={preset}
            done={done}
            closeOnPlan={!page}
          />
        </FieldGroup>
      ) : (
        <Empty preview={false}>
          <EmptyHeader>
            <EmptyTitle>Project unavailable</EmptyTitle>
            <EmptyDescription>
              This project may have been removed, or you may not have access.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button
              type="button"
              variant="outline"
              onClick={() => setChosen("")}
            >
              Choose another project
            </Button>
          </EmptyContent>
        </Empty>
      )}
    </div>
  )
}

function ProjectChoices({
  project,
  preset,
  done,
  closeOnPlan,
}: {
  project: Project
  preset: DeployPreset
  done: () => void
  closeOnPlan: boolean
}) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  const [ignorePreset, setIgnorePreset] = useState(false)
  const list = environments.data?.environments ?? []
  if (environments.status === "error")
    return (
      <QueryError
        error={environments.error}
        fallback="Environments couldn't be loaded."
        retry={environments.refetch}
      />
    )
  if (!environments.data) return <Skeleton className="h-24 w-full" />
  if (list.length === 0) {
    return (
      <p className="text-[13px] text-muted-foreground">
        {project.name} is still being set up. Its environments appear in a
        moment.
      </p>
    )
  }
  if (
    !ignorePreset &&
    preset.environment &&
    !list.some((e) => e.name === preset.environment)
  ) {
    return (
      <Empty preview={false}>
        <EmptyHeader>
          <EmptyTitle>Environment unavailable</EmptyTitle>
          <EmptyDescription>
            The selected environment no longer exists. Choose another
            environment before deploying.
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            type="button"
            variant="outline"
            onClick={() => setIgnorePreset(true)}
          >
            Choose another environment
          </Button>
        </EmptyContent>
      </Empty>
    )
  }
  return (
    <Choices
      project={project}
      environments={list}
      initial={
        list.some((e) => e.name === preset.environment)
          ? (preset.environment as string)
          : (list[0] as Environment).name
      }
      done={done}
      closeOnPlan={closeOnPlan}
    />
  )
}

function Choices({
  project,
  environments,
  initial,
  done,
  closeOnPlan,
}: {
  project: Project
  environments: Environment[]
  initial: string
  done: () => void
  closeOnPlan: boolean
}) {
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const create = useMutation(usePlansCreate)
  const { can } = useOrganization()
  const canDeploy = can("deployment:create")
  const [environment, setEnvironment] = useState(initial)
  const { busy, onPendingChange } = useDeploymentWrites(project.id)
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const [showManifest, setShowManifest] = useState(false)
  const summary = current.data?.manifest
    ? placementSummary(current.data.manifest, environment)
    : undefined
  const [error, setError] = useState("")

  const { sources, deployed, aliases, locked, chosen, toggle, body, ready } =
    useDeploymentScope(project.id, environment)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (
      busy ||
      create.isPending ||
      !canDeploy ||
      !current.data?.manifest ||
      !ready
    )
      return
    setError("")
    onPendingChange(true)
    try {
      const p = await create.mutateAsync({
        path: { id: project.id },
        body: { environment, ...body },
      })
      invalidate(usePlansList)
      if (closeOnPlan) done()
      void navigate({ to: "/deployments/$id", params: { id: p.id } })
      toast.success(`Planning ${project.name} to ${environment}`, {
        description: "Review the plan, then approve it to deploy.",
      })
    } catch (err) {
      setError(messageOf(err, "The plan couldn't start."))
    } finally {
      onPendingChange(false)
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-5">
      {sources.status === "error" && (
        <QueryError
          error={sources.error}
          fallback="Repositories couldn't be loaded."
          retry={sources.refetch}
        />
      )}
      {deployed.status === "error" && (
        <QueryError
          error={deployed.error}
          fallback="Deployed revisions couldn't be loaded."
          retry={deployed.refetch}
        />
      )}
      <div className="grid min-w-0 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_280px]">
        <div className="order-last grid min-w-0 gap-4 xl:order-first">
          <Card size="sm">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <StepNumber n={2} />
                Environment
              </CardTitle>
              <CardDescription className="text-xs">
                Where {project.name} deploys. Then choose its services below.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Field>
                <FieldLabel>Environment</FieldLabel>
                <ToggleGroup
                  disabled={busy || create.isPending}
                  variant="outline"
                  spacing={0}
                  value={[environment]}
                  onValueChange={(v) => {
                    const next = v[0]
                    if (next) setEnvironment(next)
                  }}
                  className="w-full"
                >
                  {environments.map((e) => (
                    <ToggleGroupItem
                      key={e.id}
                      value={e.name}
                      className={cn(segmented, "flex-1")}
                    >
                      {e.name}
                    </ToggleGroupItem>
                  ))}
                </ToggleGroup>
              </Field>
            </CardContent>
          </Card>
          {current.data?.manifest ? (
            <DeploymentTargets
              projectId={project.id}
              manifest={current.data.manifest}
              environments={environments}
              environment={environment}
              onEnvironment={setEnvironment}
              onPendingChange={onPendingChange}
              setup
            />
          ) : current.status === "error" && statusOf(current.error) === 404 ? (
            <SuggestedConfiguration
              projectId={project.id}
              onPendingChange={onPendingChange}
            />
          ) : current.status === "error" ? (
            <QueryError
              error={current.error}
              fallback="Deployment configuration couldn't be loaded."
              retry={current.refetch}
            />
          ) : (
            <Skeleton className="h-40" />
          )}
          {current.data && (
            <div hidden={!showManifest}>
              <ManifestEditor
                project={project}
                yaml={current.data.yaml}
                revision={current.data.revision}
                onPendingChange={onPendingChange}
              />
            </div>
          )}

          {aliases.length > 1 && (
            <Field>
              <FieldLabel>Repositories that move</FieldLabel>
              <div className="flex flex-wrap gap-2">
                {aliases.map((alias) => (
                  <Button
                    key={alias}
                    type="button"
                    size="sm"
                    variant={chosen.includes(alias) ? "secondary" : "outline"}
                    aria-pressed={chosen.includes(alias)}
                    aria-disabled={locked.has(alias) || undefined}
                    className={cn(locked.has(alias) && "cursor-not-allowed")}
                    title={
                      locked.has(alias)
                        ? `Nothing from ${alias} runs in ${environment} yet, so the plan includes it`
                        : chosen.includes(alias)
                          ? `${alias} moves to its latest commit`
                          : `${alias} stays at what's deployed`
                    }
                    onClick={() => toggle(alias)}
                  >
                    {alias}
                  </Button>
                ))}
              </div>
              <FieldDescription>
                A held repository stays at the commit {environment} runs now.
              </FieldDescription>
            </Field>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </div>
        <aside
          className="order-first grid gap-3 xl:sticky xl:top-16 xl:order-last"
          aria-label="Deployment summary"
        >
          <Card size="sm" className="hero-glow">
            <CardHeader className="hidden xl:grid">
              <CardTitle>Ready when you are</CardTitle>
              <CardDescription className="text-xs">
                {environment} ·{" "}
                {current.data
                  ? `revision ${current.data.revision}`
                  : "First deployment"}
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-3 sm:grid-cols-2 xl:grid-cols-1">
              {current.data?.manifest && (
                <>
                  <div className="hidden items-center justify-between text-[13px] xl:flex">
                    <span className="text-muted-foreground">
                      Services included
                    </span>
                    <span className="font-mono tabular-nums">
                      {summary?.services}
                    </span>
                  </div>
                  <div className="hidden items-center justify-between text-[13px] xl:flex">
                    <span className="text-muted-foreground">
                      Backing resources
                    </span>
                    <span className="font-mono tabular-nums">
                      {summary?.resources}
                    </span>
                  </div>
                </>
              )}

              <Button
                type="submit"
                className="h-10 w-full sm:col-span-2 xl:col-span-1"
                disabled={
                  create.isPending ||
                  busy ||
                  !current.data?.manifest ||
                  !canDeploy ||
                  !ready
                }
              >
                {create.isPending ? <Spinner /> : <ArrowRightIcon />}
                Review deployment
              </Button>
              <p
                role="status"
                className="text-xs text-muted-foreground sm:col-span-2 xl:col-span-1"
              >
                {busy
                  ? "Save your configuration changes to continue."
                  : "Planning makes no cloud changes. Review the plan, then approve it to deploy."}
              </p>
              {current.data && (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  aria-expanded={showManifest}
                  onClick={() => setShowManifest(!showManifest)}
                >
                  <FileCodeIcon /> {showManifest ? "Hide" : "View"} manifest
                </Button>
              )}
              <Button type="button" variant="ghost" onClick={done}>
                Cancel
              </Button>
            </CardContent>
          </Card>
        </aside>
      </div>
    </form>
  )
}

function SuggestedConfiguration({
  projectId,
  onPendingChange,
}: {
  projectId: string
  onPendingChange: (pending: boolean) => void
}) {
  const draft = useQuery(useManifestsDraft, { path: { id: projectId } })
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const { can } = useOrganization()
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
  if (!draft.data) return <Skeleton className="h-24" />
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>Start with your repository</CardTitle>
        <CardDescription>
          We've suggested services and infrastructure from your latest analysis.
          You can change every placement before planning.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <Button
          type="button"
          className="w-fit"
          disabled={save.isPending || !can("project:update")}
          onClick={() => void initialize()}
        >
          {save.isPending && <Spinner />}Use suggested configuration
        </Button>
      </CardContent>
    </Card>
  )
}

/** StepNumber marks a section of the form in the order it's filled in. */
function StepNumber({ n }: { n: number }) {
  return (
    <span
      aria-hidden="true"
      className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-primary font-mono text-[11px] text-primary-foreground"
    >
      {n}
    </span>
  )
}
