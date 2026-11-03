import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useBindingsList,
  useEnvironmentsCreate,
  useEnvironmentsDelete,
  useEnvironmentsList,
  useVariablesResolve,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { providerName } from "@/components/cloud-detail/clusters"
import {
  ActionsCell,
  Cell,
  CloudConfirm,
  CloudTable,
  CloudTableRow,
  NameCell,
  RowMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { Choice } from "@/components/cloud-targets/target-editor"
import { QueryError } from "@/components/query-error"
import { StatusPill, toneOf } from "@/components/runs"
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
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { relative } from "@/lib/format"
import { isReady } from "@/lib/projects"
import { Link } from "@/lib/router"
import { isLive, type Run, runStatusText } from "@/lib/runs"
import { useNow, usePeople } from "@/lib/use-runs"
import { environmentKinds, kindFor } from "@/lib/variables"

/** AddEnvironment adds an environment, such as staging, to a project. */
export function AddEnvironment({ project }: { project: Project }) {
  const create = useMutation(useEnvironmentsCreate)
  const invalidate = useInvalidate()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [kind, setKind] = useState<string | null>(null)
  const [error, setError] = useState("")
  const chosenKind = kind ?? kindFor(name.trim())

  const close = (next: boolean) => {
    if (!next) {
      setName("")
      setKind(null)
      setError("")
    }
    setOpen(next)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      const env = await create.mutateAsync({
        path: { id: project.id },
        body: { name: name.trim(), kind: chosenKind },
      })
      invalidate(useEnvironmentsList)
      toast.success(`Added ${env.name}`)
      close(false)
    } catch (err) {
      setError(messageOf(err, "The environment wasn't added."))
    }
  }

  return (
    <>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        Add environment
      </Button>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={(e) => void submit(e)} className="grid gap-5">
            <DialogHeader>
              <DialogTitle>Add an environment</DialogTitle>
              <DialogDescription>
                Somewhere else {project.name} deploys to, with its own
                variables.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="environment-name">Name</FieldLabel>
                <Input
                  id="environment-name"
                  value={name}
                  onChange={(e) => setName(e.target.value.toLowerCase())}
                  placeholder="staging"
                  className="font-mono"
                  autoFocus
                  required
                />
                <FieldDescription>
                  Lower case letters, digits and hyphens. It can&apos;t be
                  renamed later.
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel id="environment-kind">Kind</FieldLabel>
                <div
                  role="radiogroup"
                  aria-labelledby="environment-kind"
                  className="grid gap-2 sm:grid-cols-2"
                >
                  {environmentKinds.map((k) => (
                    <Choice
                      key={k.value}
                      checked={chosenKind === k.value}
                      onSelect={() => setKind(k.value)}
                    >
                      <span className="grid min-w-0 gap-0.5 py-1.5">
                        <span className="text-[13px] font-semibold">
                          {k.label}
                        </span>
                        <span className="text-xs font-normal text-muted-foreground">
                          {k.description}
                        </span>
                      </span>
                    </Choice>
                  ))}
                </div>
              </Field>
              {error && (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
            </FieldGroup>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => close(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || !name.trim()}>
                {create.isPending && <Spinner />}
                {name.trim() ? `Add ${name.trim()}` : "Add environment"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}

/**
 * Environments lists where a project deploys and what each is doing now:
 * its state, its last deploy and where its services run. A row waiting on
 * a plan offers Review; the rest offer Deploy.
 */
export function Environments({
  project,
  environments,
  error,
  retry,
  mayManage,
  mayDeploy,
  runs = [],
  onOpen,
}: {
  project: Project
  /** Undefined while the list loads. */
  environments?: Environment[]
  error?: unknown
  retry: () => Promise<unknown>
  mayManage: boolean
  mayDeploy: boolean
  /** The project's runs, newest first. */
  runs?: Run[]
  /** Opens another tab of the project page on one environment. */
  onOpen: (tab: "deployments" | "variables", environment: string) => void
}) {
  const now = useNow(30_000)
  const [deleting, setDeleting] = useState<Environment | null>(null)
  const list = environments ?? []
  return (
    <section aria-label="Environments" className="grid gap-2.5">
      <CloudSectionHead
        title="Environments"
        count={environments ? list.length : undefined}
        description={`Where ${project.name} runs, and what each one is doing now.`}
      >
        {mayManage && environments && <AddEnvironment project={project} />}
      </CloudSectionHead>
      {error !== undefined && !environments ? (
        <QueryError
          error={error}
          fallback="The environments couldn't be loaded."
          retry={retry}
        />
      ) : !environments ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : list.length === 0 ? (
        <CloudEmpty
          kind="compact"
          title="No environments yet"
          action={mayManage && <AddEnvironment project={project} />}
        >
          Add one, such as production or staging, to deploy {project.name}{" "}
          somewhere.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Environments"
          columns="minmax(0,1fr) 160px minmax(0,1.2fr) minmax(0,1fr) 130px"
          head={["Environment", "State", "Last deploy", "Runs on", ""]}
          minWidth={760}
        >
          {list.map((env) => (
            <EnvironmentRow
              key={env.id}
              project={project}
              environment={env}
              runs={runs.filter((r) => r.environment === env.name)}
              now={now}
              mayManage={mayManage}
              mayDeploy={mayDeploy}
              last={list.length === 1}
              onOpen={onOpen}
              onDelete={() => setDeleting(env)}
            />
          ))}
        </CloudTable>
      )}
      <DeleteEnvironment
        project={project}
        environment={deleting}
        onDone={() => setDeleting(null)}
      />
    </section>
  )
}

function EnvironmentRow({
  project,
  environment,
  runs,
  now,
  mayManage,
  mayDeploy,
  last,
  onOpen,
  onDelete,
}: {
  project: Project
  environment: Environment
  /** This environment's runs, newest first. */
  runs: Run[]
  now: number
  mayManage: boolean
  mayDeploy: boolean
  last: boolean
  onOpen: (tab: "deployments" | "variables", environment: string) => void
  onDelete: () => void
}) {
  const people = usePeople()
  const bindings = useQuery(useBindingsList, {
    path: { id: project.id },
    query: { environment: environment.name },
  })
  const services = (bindings.data?.bindings ?? []).filter(
    (b) => b.subject === "service"
  )
  const settingUp = !isReady(project.status)
  const latest = runs[0]
  const waiting = runs.filter((r) => r.status === "waiting")
  const kind =
    environmentKinds.find((k) => k.value === environment.kind)?.label ??
    environment.kind
  const providers = [...new Set(services.map((b) => providerName(b.provider)))]

  const state = (() => {
    if (settingUp) return <StatusPill tone="flight">Setting up</StatusPill>
    if (latest && (isLive(latest) || latest.status === "failed"))
      return (
        <StatusPill tone={toneOf(latest.status)}>
          {runStatusText[latest.status]}
        </StatusPill>
      )
    if (!bindings.data && bindings.status !== "error")
      return <Skeleton className="h-5 w-20 rounded-full" />
    if (services.some((b) => b.drift))
      return <StatusPill tone="waiting">Changed outside</StatusPill>
    if (services.length > 0) return <StatusPill tone="live">Live</StatusPill>
    return <StatusPill tone="quiet">Not deployed</StatusPill>
  })()

  const lastDeploy = waiting.length
    ? `${waiting.length} ${waiting.length === 1 ? "plan" : "plans"} to approve · planned ${relative(Date.parse(waiting[0].createdAt), now)}`
    : latest
      ? `${relative(Date.parse(latest.finishedAt ?? latest.createdAt), now)} by ${people(latest.createdBy).name}`
      : "Never"

  return (
    <CloudTableRow>
      <NameCell name={environment.name} meta={kind} mono />
      <Cell>{state}</Cell>
      <Cell className="truncate text-muted-foreground">
        {latest ? (
          <Link
            to="/deployments/$id"
            params={{ id: (waiting[0] ?? latest).id }}
            className="underline-offset-2 hover:text-foreground hover:underline"
          >
            {lastDeploy}
          </Link>
        ) : (
          lastDeploy
        )}
      </Cell>
      <Cell className="truncate text-muted-foreground">
        {bindings.status === "error"
          ? "Couldn't be read"
          : providers.length > 0
            ? providers.join(", ")
            : bindings.data
              ? "Nothing yet"
              : ""}
      </Cell>
      <ActionsCell>
        {waiting[0] ? (
          <Button
            size="sm"
            nativeButton={false}
            render={
              <Link to="/deployments/$id" params={{ id: waiting[0].id }} />
            }
          >
            Review
          </Button>
        ) : (
          mayDeploy &&
          !settingUp && (
            <Button
              size="sm"
              variant="outline"
              nativeButton={false}
              render={
                <Link
                  to="/deployments/new"
                  search={{
                    project: project.id,
                    environment: environment.name,
                  }}
                />
              }
            >
              Deploy
            </Button>
          )
        )}
        <RowMenu label={`More actions for ${environment.name}`}>
          <DropdownMenuItem
            onClick={() => onOpen("deployments", environment.name)}
          >
            Where services run
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={() => onOpen("variables", environment.name)}
          >
            Variables
          </DropdownMenuItem>
          {mayManage && (
            <>
              <DropdownMenuSeparator />
              {/* A project needs at least one environment. */}
              <DropdownMenuItem
                variant="destructive"
                disabled={last}
                onClick={onDelete}
              >
                {last
                  ? "Can't delete the only environment"
                  : "Delete environment"}
              </DropdownMenuItem>
            </>
          )}
        </RowMenu>
      </ActionsCell>
    </CloudTableRow>
  )
}

function DeleteEnvironment({
  project,
  environment,
  onDone,
}: {
  project: Project
  environment: Environment | null
  onDone: () => void
}) {
  const remove = useMutation(useEnvironmentsDelete)
  const invalidate = useInvalidate()

  const confirm = async () => {
    if (!environment) return
    try {
      await remove.mutateAsync({
        path: { id: project.id, envId: environment.id },
      })
      toast.success(`Deleted ${environment.name}`)
    } catch (err) {
      toast.error(messageOf(err, "The environment wasn't deleted."))
    } finally {
      onDone()
      invalidate(useEnvironmentsList)
      invalidate(useVariablesResolve)
    }
  }

  return (
    <CloudConfirm
      open={!!environment}
      onOpenChange={(open) => !open && onDone()}
      title={`Delete ${environment?.name ?? "environment"}?`}
      confirmLabel="Delete environment"
      pending={remove.isPending}
      onConfirm={() => void confirm()}
    >
      The variables set on it for {project.name} go with it, secrets included.
      Workspace-wide variables for {environment?.name} stay, for other projects.
      What runs there keeps running until you remove it in its account.
    </CloudConfirm>
  )
}
