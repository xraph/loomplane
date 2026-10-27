import { PlusIcon, Trash2Icon } from "lucide-react"
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
import { ServiceAddress } from "@/components/service-address"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { isReady } from "@/lib/projects"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Card } from "@/components/ui/card"
import { Choice } from "@/components/cloud-targets/target-editor"
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
import { Input } from "@/components/ui/input"
import { StatusPill, Thread, toneOf } from "@/components/runs"
import { Spinner } from "@/components/ui/spinner"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { relative } from "@/lib/format"
import { Link } from "@/lib/router"
import { isLive, type Run, runStatusText } from "@/lib/runs"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"
import { environmentKinds, kindFor } from "@/lib/variables"

// Every new project starts with these. They stand in while the real list
// loads.
const defaultEnvironments = ["production", "preview"]

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
      <Button
        variant="outline"
        onClick={() => setOpen(true)}
        aria-label="Add environment"
        className="h-auto min-h-[120px] flex-col gap-1.5 rounded-[14px] border-dashed bg-transparent text-[13.5px] whitespace-normal shadow-none hover:bg-subtle"
      >
        <span className="flex size-[30px] items-center justify-center rounded-lg bg-brand-tint text-brand-ink">
          <PlusIcon />
        </span>
        Add environment
        <span className="text-xs font-normal text-muted-foreground">
          Staging, preview or development
        </span>
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
 * EnvironmentCards shows where a project deploys, one card each, and lets
 * someone who may add environments and remove all but the last.
 */
export function EnvironmentCards({
  project,
  environments,
  loading,
  mayManage,
  runs = [],
}: {
  project: Project
  environments: Environment[]
  /** Stands in the defaults every project starts with while the list loads. */
  loading: boolean
  mayManage: boolean
  /** The project's runs, newest first; each card shows its environment's latest. */
  runs?: Run[]
}) {
  const now = useNow(30_000)
  const cards = loading
    ? defaultEnvironments.map((name) => ({ id: name, name, kind: name }))
    : environments
  const count = cards.length + (mayManage ? 1 : 0)
  return (
    <div
      className={cn(
        "grid gap-3",
        count > 1 && "md:grid-cols-2",
        count > 2 && "xl:grid-cols-3"
      )}
    >
      {cards.map((env) => {
        const real = environments.find((e) => e.id === env.id)
        const run = runs.find((r) => r.environment === env.name)
        const settingUp = loading || !isReady(project.status)
        return (
          <Card
            key={env.id}
            size="sm"
            className="min-w-0 gap-0 rounded-[14px] py-0"
          >
            <div className="flex min-w-0 items-center gap-2 px-3.5 pt-3 pb-2">
              <h3 className="truncate text-[14px] font-semibold">{env.name}</h3>
              <Badge
                variant="outline"
                className="h-5 rounded-md px-1.5 text-[11.5px] font-normal text-muted-foreground"
              >
                {environmentKinds.find((k) => k.value === env.kind)?.label ??
                  env.kind}
              </Badge>
              <span className="flex-1" />
              {!settingUp && run && (
                <StatusPill tone={toneOf(run.status)}>
                  {runStatusText[run.status]}
                </StatusPill>
              )}
              {mayManage && real && (
                <DeleteEnvironment
                  project={project}
                  environment={real}
                  last={environments.length === 1}
                />
              )}
            </div>
            {settingUp ? (
              <span className="flex items-center gap-2 px-3.5 pb-3 text-[12.5px] text-muted-foreground">
                <span className="h-1.5 w-3 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
                Setting up
              </span>
            ) : (
              <>
                <LatestRun run={run} now={now} />
                <div className="border-t px-3.5 pt-2 pb-3">
                  <Deployed project={project} environment={env.name} />
                </div>
              </>
            )}
          </Card>
        )
      })}
      {mayManage && !loading && <AddEnvironment project={project} />}
    </div>
  )
}

function DeleteEnvironment({
  project,
  environment,
  last,
}: {
  project: Project
  environment: Environment
  last: boolean
}) {
  const remove = useMutation(useEnvironmentsDelete)
  const invalidate = useInvalidate()
  // Controlled: Base UI's AlertDialogAction doesn't close the dialog.
  const [open, setOpen] = useState(false)

  if (last) {
    // aria-disabled, not disabled: a disabled button never shows the
    // tooltip that says why.
    return (
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-xs"
              className="text-muted-foreground opacity-50"
              aria-label={`Delete ${environment.name}`}
              aria-disabled="true"
            />
          }
        >
          <Trash2Icon />
        </TooltipTrigger>
        <TooltipContent>
          A project needs at least one environment
        </TooltipContent>
      </Tooltip>
    )
  }

  const confirm = async () => {
    try {
      await remove.mutateAsync({
        path: { id: project.id, envId: environment.id },
      })
      toast.success(`Deleted ${environment.name}`)
    } catch (err) {
      toast.error(messageOf(err, "The environment wasn't deleted."))
    } finally {
      setOpen(false)
      invalidate(useEnvironmentsList)
      invalidate(useVariablesResolve)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger
        render={
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground"
            aria-label={`Delete ${environment.name}`}
            disabled={remove.isPending}
          />
        }
      >
        <Trash2Icon />
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {environment.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The variables set on it for {project.name} go with it, secrets
            included. Workspace-wide variables for {environment.name} stay,
            for other projects.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => void confirm()}
          >
            {remove.isPending && <Spinner />}
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * Deployed lists what runs in an environment and where each service
 * answers, from what the last deployments recorded.
 */
/** LatestRun is an environment's newest deployment as a thread. */
function LatestRun({ run, now }: { run?: Run; now: number }) {
  if (!run) return null
  return (
    <Link
      to="/deployments/$id"
      params={{ id: run.id }}
      className="flex min-w-0 items-center gap-2 px-3.5 pb-2.5 text-[12.5px] text-muted-foreground transition-colors hover:text-foreground"
    >
      <Thread steps={run.thread} size="sm" className="w-[72px] shrink-0" />
      <span className="truncate">{run.phase}</span>
      <span className="ml-auto shrink-0">
        {isLive(run)
          ? runStatusText[run.status]
          : relative(Date.parse(run.finishedAt ?? run.createdAt), now)}
      </span>
    </Link>
  )
}

function Deployed({
  project,
  environment,
}: {
  project: Project
  environment: string
}) {
  const bindings = useQuery(useBindingsList, {
    path: { id: project.id },
    query: { environment },
  })
  const services = (bindings.data?.bindings ?? []).filter(
    (b) => b.subject === "service"
  )
  if (services.length === 0) {
    return (
      <span className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
        <span className="h-1.5 w-3 rounded-full bg-warp" />
        {bindings.data ? "Not deployed yet" : "Checking"}
      </span>
    )
  }
  return (
    <ul className="grid gap-1.5 text-[12.5px]">
      {services.map((b) => (
        <li
          key={b.id}
          className="flex min-w-0 items-center gap-2"
          title={
            b.drift ? `Changed outside Loomplane: ${b.driftMessage}` : undefined
          }
        >
          <span
            className={cn(
              "h-1.5 w-3 shrink-0 rounded-full",
              b.drift ? "bg-weld" : "bg-overdye"
            )}
          />
          <span className="font-medium">{b.name}</span>
          {b.drift && (
            <span className="shrink-0 text-weld-ink">
              {b.drift === "missing"
                ? "deleted outside Loomplane"
                : "changed outside Loomplane"}
            </span>
          )}
          {b.url ? (
            <ServiceAddress
              address={b.url}
              className="truncate text-muted-foreground"
            />
          ) : (
            <span className="text-muted-foreground">on {b.provider}</span>
          )}
        </li>
      ))}
    </ul>
  )
}
