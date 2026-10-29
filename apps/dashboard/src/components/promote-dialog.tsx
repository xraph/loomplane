// Promote: plan one environment to run exactly what another one runs, such
// as staging into production. It's a plan like any other, so nothing moves
// until someone approves it, and Loomplane reuses the images the source
// environment already runs wherever it can.
import { ArrowRightIcon } from "@phosphor-icons/react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useDeployedList,
  useEnvironmentsList,
  usePlansCreate,
  usePlansList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  DeployedRepository,
  Environment,
  Project,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { useNavigate } from "@/lib/router"

export function PromoteDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {/* Mounted only while open, so each opening starts from the defaults. */}
        {open && <PromoteForm done={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function PromoteForm({ done }: { done: () => void }) {
  const list = useQuery(useProjectsList)
  const projects = list.data?.projects ?? []
  const [chosen, setChosen] = useState("")
  const project = projects.find((p) => p.id === chosen) ?? projects[0]
  return (
    <>
      <DialogHeader>
        <DialogTitle>Promote</DialogTitle>
        <DialogDescription>
          Plan an environment to run exactly what another one runs. Loomplane
          reuses the images it already built where it can, and nothing changes
          until someone approves the plan.
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
      ) : !project ? (
        <p className="text-[13px] text-muted-foreground">
          There are no projects to promote yet.
        </p>
      ) : (
        <div className="grid gap-4">
          {projects.length > 1 && (
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
                  {projects.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {/* Keyed by project: its environments differ. */}
          <ProjectEnvironments key={project.id} project={project} done={done} />
        </div>
      )}
    </>
  )
}

/**
 * defaults picks staging into production when the project has both, and
 * otherwise the last two environments in the order the project lists them.
 */
export function promoteDefaults(envs: Environment[]): {
  from: string
  to: string
} {
  const names = envs.map((e) => e.name)
  const to = names.includes("production") ? "production" : (names.at(-1) ?? "")
  const from = names.includes("staging")
    ? "staging"
    : ([...names].reverse().find((n) => n !== to) ?? "")
  return { from: from === to ? "" : from, to }
}

function ProjectEnvironments({
  project,
  done,
}: {
  project: Project
  done: () => void
}) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  if (environments.status === "error")
    return (
      <QueryError
        error={environments.error}
        fallback="Environments couldn't be loaded."
        retry={environments.refetch}
      />
    )
  if (!environments.data) return <Skeleton className="h-32 w-full" />
  const envs = environments.data.environments
  if (envs.length < 2)
    return (
      <p className="text-[13px] text-muted-foreground">
        Promoting moves what one environment runs into another, and{" "}
        {project.name} has {envs.length === 1 ? "only one" : "none yet"}.
      </p>
    )
  return <Choices project={project} environments={envs} done={done} />
}

function EnvironmentSelect({
  label,
  value,
  environments,
  onChange,
  disabled,
}: {
  label: string
  value: string
  environments: Environment[]
  onChange: (name: string) => void
  disabled?: boolean
}) {
  return (
    <Field className="min-w-0">
      <FieldLabel>{label}</FieldLabel>
      <Select
        items={environments.map((e) => ({ label: e.name, value: e.name }))}
        value={value}
        onValueChange={(v) => onChange(String(v))}
      >
        <SelectTrigger
          className="w-full"
          aria-label={label}
          disabled={disabled}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {environments.map((e) => (
            <SelectItem key={e.id} value={e.name}>
              {e.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

function Choices({
  project,
  environments,
  done,
}: {
  project: Project
  environments: Environment[]
  done: () => void
}) {
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const { can } = useOrganization()
  const create = useMutation(usePlansCreate)
  const initial = promoteDefaults(environments)
  const [from, setFrom] = useState(initial.from)
  const [to, setTo] = useState(initial.to)
  const [error, setError] = useState("")
  const source = useQuery(useDeployedList, {
    path: { id: project.id, environment: from },
  })
  const target = useQuery(useDeployedList, {
    path: { id: project.id, environment: to },
  })
  const runs = source.data?.sources ?? []
  const same = from === to
  const ready =
    !same && runs.length > 0 && target.data !== undefined && !create.isPending

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready || !can("deployment:create")) return
    setError("")
    try {
      const p = await create.mutateAsync({
        path: { id: project.id },
        body: { environment: to, from },
      })
      invalidate(usePlansList)
      done()
      void navigate({ to: "/deployments/$id", params: { id: p.id } })
      toast.success(`Planning ${project.name}: ${from} to ${to}`, {
        description: "Review the plan, then approve it to promote.",
      })
    } catch (err) {
      setError(messageOf(err, "The promotion couldn't be planned."))
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-4">
      <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-end gap-2">
        <EnvironmentSelect
          label="From"
          value={from}
          environments={environments}
          onChange={setFrom}
          disabled={create.isPending}
        />
        <ArrowRightIcon
          aria-hidden="true"
          className="mb-2.5 size-4 text-muted-foreground"
        />
        <EnvironmentSelect
          label="To"
          value={to}
          environments={environments}
          onChange={setTo}
          disabled={create.isPending}
        />
      </div>
      {same ? (
        <p className="text-[13px] text-muted-foreground">
          Pick two different environments.
        </p>
      ) : source.status === "error" ? (
        <QueryError
          error={source.error}
          fallback={`What ${from} runs couldn't be loaded.`}
          retry={source.refetch}
        />
      ) : target.status === "error" ? (
        <QueryError
          error={target.error}
          fallback={`What ${to} runs couldn't be loaded.`}
          retry={target.refetch}
        />
      ) : !source.data || !target.data ? (
        <Skeleton className="h-20 w-full rounded-lg" />
      ) : runs.length === 0 ? (
        <div className="grid gap-1 rounded-lg bg-subtle px-3 py-2.5 ring-1 ring-border">
          <p className="text-[13px] font-medium">
            {from} hasn&apos;t been deployed yet
          </p>
          <p className="text-[12.5px] text-muted-foreground">
            Deploy it first, then promote what it runs.
          </p>
        </div>
      ) : (
        <Moves from={from} to={to} source={runs} target={target.data.sources} />
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
        <Button type="submit" disabled={!ready || !can("deployment:create")}>
          {create.isPending && <Spinner />}
          Promote {from || "…"} to {to || "…"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** Moves lists each repository's commit in the target, then the source's. */
function Moves({
  from,
  to,
  source,
  target,
}: {
  from: string
  to: string
  source: DeployedRepository[]
  target: DeployedRepository[]
}) {
  const rows = [...source].sort((a, b) => a.alias.localeCompare(b.alias))
  return (
    <div className="grid gap-2">
      <p className="text-[12.5px] text-muted-foreground">
        {to} moves to the commits {from} runs. Repositories {from} doesn&apos;t
        run stay as {to} has them.
      </p>
      <ul
        aria-label="What moves"
        className="grid divide-y rounded-lg ring-1 ring-border"
      >
        {rows.map((s) => {
          const now = target.find(
            (t) => t.sourceId === s.sourceId || t.alias === s.alias
          )
          const unchanged = now?.commitSha === s.commitSha
          return (
            <li
              key={s.sourceId || s.alias}
              className="flex items-center gap-2 px-3 py-2 text-[13px]"
            >
              <span className="min-w-0 flex-1 truncate font-medium">
                {s.alias}
              </span>
              {unchanged ? (
                <>
                  <code className="font-mono text-[12px] text-muted-foreground">
                    {s.commitSha.slice(0, 7)}
                  </code>
                  <span className="text-[12.5px] text-muted-foreground">
                    already there
                  </span>
                </>
              ) : (
                <>
                  {now ? (
                    <code className="font-mono text-[12px] text-muted-foreground">
                      {now.commitSha.slice(0, 7)}
                    </code>
                  ) : (
                    <span className="text-[12.5px] text-muted-foreground">
                      new
                    </span>
                  )}
                  <ArrowRightIcon
                    aria-hidden="true"
                    className="size-3.5 shrink-0 text-muted-foreground"
                  />
                  <code className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[12px]">
                    {s.commitSha.slice(0, 7)}
                  </code>
                </>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
