import { PlusIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useEnvironmentsCreate,
  useEnvironmentsDelete,
  useEnvironmentsList,
  useVariablesResolve,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
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
import { Badge } from "@/components/ui/badge"
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
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <PlusIcon />
        Add environment
      </Button>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={(e) => void submit(e)}>
            <DialogHeader>
              <DialogTitle>Add an environment</DialogTitle>
              <DialogDescription>
                Somewhere else {project.name} deploys to, with its own
                variables.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-4">
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
 * EnvironmentList shows a project's environments with what each is for,
 * and lets someone who may remove all but the last.
 */
export function EnvironmentList({
  project,
  environments,
  mayManage,
}: {
  project: Project
  environments: Environment[]
  mayManage: boolean
}) {
  return (
    <ul className="flex flex-wrap gap-2">
      {environments.map((env) => (
        <li
          key={env.id}
          className="flex items-center gap-2 rounded-lg border py-1 pr-1 pl-3 text-sm"
        >
          <span className="font-medium">{env.name}</span>
          {env.kind !== env.name && (
            <Badge variant="secondary">{env.kind}</Badge>
          )}
          {mayManage && (
            <DeleteEnvironment
              project={project}
              environment={env}
              last={environments.length === 1}
            />
          )}
        </li>
      ))}
    </ul>
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
            size="xs"
            disabled={last || remove.isPending}
            title={
              last
                ? "A project needs at least one environment"
                : `Delete ${environment.name}`
            }
          />
        }
      >
        Delete
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
