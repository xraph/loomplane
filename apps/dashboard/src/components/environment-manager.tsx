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
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
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
        className="h-auto min-h-28 flex-col gap-2 rounded-xl border-dashed bg-transparent text-muted-foreground shadow-none hover:bg-muted/40"
      >
        <PlusIcon />
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
                  autoFocus
                  required
                />
                <FieldDescription>
                  Lower case letters, digits and hyphens. It can&apos;t be
                  renamed later.
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel>Kind</FieldLabel>
                <Select
                  items={environmentKinds.map((k) => ({
                    label: k.label,
                    value: k.value,
                  }))}
                  value={chosenKind}
                  onValueChange={(v) => setKind(v as string)}
                >
                  <SelectTrigger aria-label="Kind" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {environmentKinds.map((k) => (
                      <SelectItem key={k.value} value={k.value}>
                        {k.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FieldDescription>
                  {environmentKinds.find((k) => k.value === chosenKind)
                    ?.description ?? ""}
                </FieldDescription>
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
                Add environment
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
}: {
  project: Project
  environments: Environment[]
  /** Stands in the defaults every project starts with while the list loads. */
  loading: boolean
  mayManage: boolean
}) {
  const cards = loading
    ? defaultEnvironments.map((name) => ({ id: name, name, kind: name }))
    : environments
  const count = cards.length + (mayManage ? 1 : 0)
  return (
    <div
      className={cn(
        "grid gap-4",
        count > 1 && "md:grid-cols-2",
        count > 2 && "xl:grid-cols-3"
      )}
    >
      {cards.map((env) => {
        const real = environments.find((e) => e.id === env.id)
        return (
          <Card key={env.id} className="min-w-0">
            <CardHeader>
              <CardTitle className="capitalize">{env.name}</CardTitle>
              {env.kind !== env.name && (
                <CardDescription>
                  {environmentKinds.find((k) => k.value === env.kind)?.label ??
                    env.kind}
                </CardDescription>
              )}
              {mayManage && real && (
                <CardAction>
                  <DeleteEnvironment
                    project={project}
                    environment={real}
                    last={environments.length === 1}
                  />
                </CardAction>
              )}
            </CardHeader>
            <CardContent>
              {loading || !isReady(project.status) ? (
                <span className="flex items-center gap-2 text-xs text-muted-foreground">
                  <span className="h-1.5 w-3 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
                  Setting up
                </span>
              ) : (
                <Deployed project={project} environment={env.name} />
              )}
            </CardContent>
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
            included. Organization-wide variables for {environment.name} stay,
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
      <span className="flex items-center gap-2 text-xs text-muted-foreground">
        <span className="h-1.5 w-3 rounded-full bg-warp" />
        {bindings.data ? "Not deployed yet" : "Checking"}
      </span>
    )
  }
  return (
    <ul className="grid gap-1.5 text-xs">
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
            <span className="text-weld-ink shrink-0">
              {b.drift === "missing"
                ? "deleted outside Loomplane"
                : "changed outside Loomplane"}
            </span>
          )}
          {b.url ? (
            <a
              href={b.url}
              target="_blank"
              rel="noreferrer"
              className="truncate text-muted-foreground underline-offset-4 hover:underline"
            >
              {b.url.replace(/^https?:\/\//, "")}
            </a>
          ) : (
            <span className="text-muted-foreground">on {b.provider}</span>
          )}
        </li>
      ))}
    </ul>
  )
}
