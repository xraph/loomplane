import { LockIcon, PlusIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesGraph,
  useAnalysesList,
  useProjectVariablesCreate,
  useProjectVariablesList,
  useVariablesDelete,
  useVariablesResolve,
  useVariablesUpdate,
} from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  Project,
  ResolvedVariable,
  Variable,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  type VariableInput,
  VariableDialog,
} from "@/components/variable-dialog"
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
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { latestSucceeded } from "@/lib/architecture"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"
import { layerLabel, ownLayer, reachLabel, type Target } from "@/lib/variables"

interface ServiceOption {
  id: string
  name: string
}

/**
 * ProjectVariables is what a project's services run with, in one
 * environment at a time: each key's value and the layer it comes from,
 * organization, project or service. Values are set on the layer you're
 * looking at, and an inherited one is overridden there.
 */
export function ProjectVariables({
  project,
  environments,
}: {
  project: Project
  environments: Environment[]
}) {
  const analyses = useQuery(useAnalysesList, { path: { id: project.id } })
  const latest = latestSucceeded(analyses.data?.analyses ?? [])
  if (latest) {
    return (
      <WithServices
        analysisID={latest.id}
        project={project}
        environments={environments}
      />
    )
  }
  return <Panel project={project} environments={environments} services={[]} />
}

function WithServices({
  analysisID,
  project,
  environments,
}: {
  analysisID: string
  project: Project
  environments: Environment[]
}) {
  const graph = useQuery(useAnalysesGraph, { path: { id: analysisID } })
  const services = (graph.data?.graph.services ?? [])
    .filter((s) => s.deployable)
    .map((s) => ({ id: s.id, name: s.name }))
  return (
    <Panel project={project} environments={environments} services={services} />
  )
}

function Panel({
  project,
  environments,
  services,
}: {
  project: Project
  environments: Environment[]
  services: ServiceOption[]
}) {
  const { can } = useOrganization()
  const mayManage = can("variable:manage")
  const [chosenEnv, setEnv] = useState("")
  const [chosenService, setService] = useState("")
  // Fall back when the chosen one is deleted, instead of syncing state in
  // an effect.
  const env = environments.some((e) => e.name === chosenEnv)
    ? chosenEnv
    : (environments[0]?.name ?? "")
  const service = services.some((s) => s.id === chosenService)
    ? chosenService
    : ""
  const target: Target = {
    service,
    serviceName: services.find((s) => s.id === service)?.name,
  }
  const [dialog, setDialog] = useState<
    | { kind: "create"; presetKey?: string }
    | { kind: "edit"; variable: Variable }
    | null
  >(null)

  const create = useMutation(useProjectVariablesCreate)
  const update = useMutation(useVariablesUpdate)
  const invalidate = useInvalidate()
  const refresh = () => {
    invalidate(useVariablesResolve)
    invalidate(useProjectVariablesList)
  }

  const save = async (input: VariableInput) => {
    if (dialog?.kind === "edit") {
      await update.mutateAsync({
        path: { id: dialog.variable.id },
        body: { value: input.value, secret: input.secret },
      })
      toast.success(`Saved ${dialog.variable.key}`)
    } else {
      await create.mutateAsync({
        path: { id: project.id },
        body: {
          key: input.key,
          value: input.value,
          secret: input.secret,
          environment: input.environment || undefined,
          service: service || undefined,
        },
      })
      toast.success(`Set ${input.key}`)
    }
    refresh()
  }

  const where = target.serviceName
    ? `for ${target.serviceName}`
    : "for the whole project"

  return (
    <Card>
      <CardHeader>
        <CardTitle>Variables</CardTitle>
        <CardDescription>
          What {project.name} runs with. A value set for a service beats one set
          for the project, which beats the organization&apos;s, and a value for
          one environment beats one for every environment.
        </CardDescription>
        {mayManage && env && (
          <CardAction>
            <Button size="sm" onClick={() => setDialog({ kind: "create" })}>
              <PlusIcon />
              Set variable
            </Button>
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-3">
          <Select
            items={environments.map((e) => ({ label: e.name, value: e.name }))}
            value={env}
            onValueChange={(v) => setEnv(v as string)}
          >
            <SelectTrigger aria-label="Environment" className="min-w-40">
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
          <Select
            items={[
              { label: "Whole project", value: "" },
              ...services.map((s) => ({ label: s.name, value: s.id })),
            ]}
            value={service}
            onValueChange={(v) => setService(v as string)}
          >
            <SelectTrigger aria-label="Service" className="min-w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Whole project</SelectItem>
              {services.map((s) => (
                <SelectItem key={s.id} value={s.id}>
                  {s.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {services.length === 0 && (
          <p className="text-xs text-muted-foreground">
            Analyze the repository under Architecture to set variables for one
            service.
          </p>
        )}
        {env && (
          <Resolved
            project={project}
            environment={env}
            target={target}
            mayManage={mayManage}
            onEdit={(variable) => setDialog({ kind: "edit", variable })}
            onOverride={(key) => setDialog({ kind: "create", presetKey: key })}
          />
        )}
      </CardContent>
      {env && (
        <VariableDialog
          open={dialog !== null}
          onOpenChange={(open) => !open && setDialog(null)}
          title={
            dialog?.kind === "edit"
              ? `Change ${dialog.variable.key}`
              : dialog?.presetKey
                ? `Override ${dialog.presetKey} ${where}`
                : `Set a variable ${where}`
          }
          description={
            dialog?.kind === "edit"
              ? `${layerLabel(dialog.variable)} variable, ${reachLabel(dialog.variable)}.`
              : `Set ${where}. It applies in every environment, or only in ${env}.`
          }
          environments={{ mode: "project", current: env }}
          editing={dialog?.kind === "edit" ? dialog.variable : undefined}
          presetKey={dialog?.kind === "create" ? dialog.presetKey : undefined}
          submit={save}
        />
      )}
    </Card>
  )
}

function Resolved({
  project,
  environment,
  target,
  mayManage,
  onEdit,
  onOverride,
}: {
  project: Project
  environment: string
  target: Target
  mayManage: boolean
  onEdit: (v: Variable) => void
  onOverride: (key: string) => void
}) {
  const resolved = useQuery(useVariablesResolve, {
    path: { id: project.id },
    query: { environment, service: target.service || undefined },
  })
  if (resolved.status === "error") {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(resolved.error, "The variables couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  }
  if (!resolved.data) return <Skeleton className="h-24 w-full" />
  const rows = resolved.data.variables
  if (rows.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        Nothing set for {target.serviceName ?? "this project"} in {environment}{" "}
        yet, here or organization-wide.
      </p>
    )
  }
  return (
    <div className="overflow-hidden rounded-lg border">
      <Table>
        <TableHeader className="bg-muted">
          <TableRow>
            <TableHead>Key</TableHead>
            <TableHead>Value</TableHead>
            <TableHead>From</TableHead>
            {mayManage && (
              <TableHead className="w-0">
                <span className="sr-only">Actions</span>
              </TableHead>
            )}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((rv) => (
            <Row
              key={rv.key}
              row={rv}
              target={target}
              mayManage={mayManage}
              onEdit={onEdit}
              onOverride={onOverride}
            />
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function Row({
  row,
  target,
  mayManage,
  onEdit,
  onOverride,
}: {
  row: ResolvedVariable
  target: Target
  mayManage: boolean
  onEdit: (v: Variable) => void
  onOverride: (key: string) => void
}) {
  const v = row.variable
  const own = ownLayer(v, target)
  return (
    <TableRow>
      <TableCell className="font-mono text-sm">{row.key}</TableCell>
      <TableCell className="max-w-xs">
        <Value variable={v} />
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant={own ? "default" : "secondary"}>{layerLabel(v)}</Badge>
          <span className="text-xs text-muted-foreground">{reachLabel(v)}</span>
        </div>
        {row.overrides.length > 0 && (
          <div className="mt-1 text-xs text-muted-foreground">
            overrides{" "}
            {row.overrides
              .map((o) => `${layerLabel(o).toLowerCase()} (${reachLabel(o)})`)
              .join(", ")}
          </div>
        )}
      </TableCell>
      {mayManage && (
        <TableCell>
          <div className="flex justify-end gap-1">
            {own ? (
              <>
                <Button variant="ghost" size="xs" onClick={() => onEdit(v)}>
                  Edit
                </Button>
                <DeleteVariable variable={v} />
              </>
            ) : (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => onOverride(row.key)}
              >
                Override
              </Button>
            )}
          </div>
        </TableCell>
      )}
    </TableRow>
  )
}

/** Value shows a variable's value, or that it's a secret. */
export function Value({ variable }: { variable: Variable }) {
  if (variable.secret) {
    return (
      <span className="inline-flex items-center gap-1 text-sm text-muted-foreground">
        <LockIcon className="size-3.5" aria-hidden="true" />
        Secret
      </span>
    )
  }
  return (
    <code
      className={cn(
        "block truncate font-mono text-sm",
        variable.value === "" && "text-muted-foreground"
      )}
      title={variable.value}
    >
      {variable.value === "" ? "(empty)" : variable.value}
    </code>
  )
}

/** DeleteVariable removes a variable after asking. */
export function DeleteVariable({ variable }: { variable: Variable }) {
  const remove = useMutation(useVariablesDelete)
  const invalidate = useInvalidate()
  // Controlled: Base UI's AlertDialogAction doesn't close the dialog.
  const [open, setOpen] = useState(false)

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: variable.id } })
      toast.success(`Deleted ${variable.key}`)
    } catch (err) {
      toast.error(messageOf(err, "The variable wasn't deleted."))
    } finally {
      setOpen(false)
      invalidate(useVariablesResolve)
      invalidate(useProjectVariablesList)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger
        render={
          <Button variant="ghost" size="xs" disabled={remove.isPending} />
        }
      >
        Delete
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {variable.key}?</AlertDialogTitle>
          <AlertDialogDescription>
            {variable.secret ? "Its secret value is destroyed. " : ""}
            Anything it was overriding applies again.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => void confirm()}
          >
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
