// New deployment: pick a project and an environment, and Loomplane plans
// the change. A deployment always starts as a plan, so nothing touches a
// cloud until someone approves what the plan says it will do.
import { WorkflowIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import {
  useDeployedList,
  useEnvironmentsList,
  usePlansCreate,
  usePlansList,
  useProjectsList,
  useSourcesList,
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
  DialogFooter,
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
import { chosenScope, lockedOn, scopeBody, toggleScope } from "@/lib/plans"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

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
          <DeployForm
            key={JSON.stringify(preset)}
            preset={preset}
            done={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DeployForm({
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
  const project =
    projects.find((p) => p.id === chosen) ?? projects[0] ?? undefined

  return (
    <div className="grid gap-5">
      <DialogHeader>
        <DialogTitle>New deployment</DialogTitle>
        <DialogDescription>
          Loomplane plans the change first, so you see what it will touch before
          anything runs. Nothing changes in a cloud until the plan is approved.
        </DialogDescription>
      </DialogHeader>
      {list.status === "error" ? (
        <QueryError
          error={list.error}
          fallback="Projects couldn't be loaded."
          retry={list.refetch}
        />
      ) : !list.data ? (
        <Skeleton className="h-40 w-full rounded-xl" />
      ) : projects.length === 0 ? (
        <Empty>
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
      ) : (
        project && (
          <FieldGroup>
            <Field>
              <FieldLabel>Project</FieldLabel>
              <Select
                items={projects.map((p) => ({ label: p.name, value: p.id }))}
                value={project.id}
                onValueChange={(v) => setChosen(String(v))}
              >
                <SelectTrigger className="w-full" aria-label="Project">
                  <SelectValue>
                    {(v: string) => {
                      const p = projects.find((x) => x.id === v)
                      if (!p) return v
                      const s = projectStyle(p)
                      return (
                        <span className="flex items-center gap-2">
                          <ProjectIcon
                            icon={s.icon}
                            color={s.color}
                            size="xs"
                          />
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
            />
          </FieldGroup>
        )
      )}
    </div>
  )
}

function ProjectChoices({
  project,
  preset,
  done,
}: {
  project: Project
  preset: DeployPreset
  done: () => void
}) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
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
      <p className="text-sm text-muted-foreground">
        {project.name} is still being set up. Its environments appear in a
        moment.
      </p>
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
    />
  )
}

function Choices({
  project,
  environments,
  initial,
  done,
}: {
  project: Project
  environments: Environment[]
  initial: string
  done: () => void
}) {
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const create = useMutation(usePlansCreate)
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  const [environment, setEnvironment] = useState(initial)
  const deployed = useQuery(useDeployedList, {
    path: { id: project.id, environment },
  })
  // null means every repository moves, including one added later.
  const [moving, setMoving] = useState<string[] | null>(null)
  const [error, setError] = useState("")

  const aliases = (sources.data?.sources ?? [])
    .filter((s) => !s.removedAt)
    .map((s) => s.alias)
  // Until what's deployed is known, every repository moves: a plan never
  // asks to hold something that isn't there.
  const running = deployed.data?.sources.map((d) => d.alias)
  const locked =
    running !== undefined ? lockedOn(aliases, running) : new Set(aliases)
  const chosen = chosenScope(aliases, moving, locked)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      const p = await create.mutateAsync({
        path: { id: project.id },
        body: { environment, ...scopeBody(aliases, chosen) },
      })
      invalidate(usePlansList)
      done()
      void navigate({ to: "/deployments/$id", params: { id: p.id } })
      toast.success(`Planning ${project.name} to ${environment}`, {
        description: "Review the plan, then approve it to deploy.",
      })
    } catch (err) {
      setError(messageOf(err, "The plan couldn't start."))
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
      <Field>
        <FieldLabel>Environment</FieldLabel>
        <ToggleGroup
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
                className={cn(
                  "rounded-full",
                  locked.has(alias) && "cursor-not-allowed"
                )}
                title={
                  locked.has(alias)
                    ? `Nothing from ${alias} runs in ${environment} yet, so the plan includes it`
                    : chosen.includes(alias)
                      ? `${alias} moves to its latest commit`
                      : `${alias} stays at what's deployed`
                }
                onClick={() =>
                  setMoving(toggleScope(aliases, chosen, alias, locked))
                }
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
      <DialogFooter>
        <Button type="button" variant="outline" onClick={done}>
          Cancel
        </Button>
        <Button
          type="submit"
          disabled={
            create.isPending ||
            !sources.data ||
            !deployed.data ||
            sources.status === "error" ||
            deployed.status === "error" ||
            (aliases.length > 0 && chosen.length === 0)
          }
        >
          {create.isPending ? <Spinner /> : <WorkflowIcon />}
          Plan deployment
        </Button>
      </DialogFooter>
    </form>
  )
}
