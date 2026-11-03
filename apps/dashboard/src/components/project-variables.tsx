import { FileUpIcon, LockIcon, PlusIcon, SparklesIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useProjectsGraph,
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
  Graph,
  Variable,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  type VariableInput,
  VariableDialog,
} from "@/components/variable-dialog"
import { type ImportSource, VariableImport } from "@/components/variable-import"
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
import { Card } from "@/components/ui/card"
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
import { detected } from "@/lib/env-import"
import { useProjectCan } from "@/lib/project-permissions"
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
  environment,
}: {
  project: Project
  environments: Environment[]
  /** The environment to open on, such as one chosen from its row. */
  environment?: string
}) {
  // The composed graph names services the way the API checks them, with
  // the alias of the repository each comes from.
  const view = useQuery(useProjectsGraph, { path: { id: project.id } })
  const graph = view.data?.graph
  // Two repositories can each have a service of the same name, so the
  // repository is part of the name once there is more than one.
  const several = (graph?.sources?.length ?? 0) > 1
  const services = (graph?.services ?? [])
    .filter((s) => s.deployable)
    .map((s) => ({
      id: s.id,
      name: several && s.source ? `${s.name} (${s.source})` : s.name,
    }))
  return (
    <Panel
      project={project}
      environments={environments}
      services={services}
      graph={graph}
      initialEnvironment={environment}
    />
  )
}

function Panel({
  project,
  environments,
  services,
  graph,
  initialEnvironment = "",
}: {
  project: Project
  environments: Environment[]
  services: ServiceOption[]
  /** The project's graph: the variables the analysis found each service reading. */
  graph?: Graph
  initialEnvironment?: string
}) {
  const can = useProjectCan(project.id)
  const mayManage = can("variable:manage")
  const [chosenEnv, setEnv] = useState(initialEnvironment)
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

  const [importing, setImporting] = useState<ImportSource | null>(null)
  // The same query the table below runs, so it's one request: what's set
  // already isn't offered again.
  const resolved = useQuery(useVariablesResolve, {
    path: { id: project.id },
    query: { environment: env, service: service || undefined },
  })
  const set = new Set((resolved.data?.variables ?? []).map((v) => v.key))
  const found = detected(graph, service)
  const unset = resolved.data
    ? found.filter((v) => !set.has(v.name) && !v.providedBy)
    : []

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
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          items={environments.map((e) => ({ label: e.name, value: e.name }))}
          value={env}
          onValueChange={(v) => setEnv(v as string)}
        >
          <SelectTrigger size="sm" aria-label="Environment" className="w-44">
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
          <SelectTrigger size="sm" aria-label="Service" className="w-48">
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
        {mayManage && env && (
          <Button
            size="sm"
            variant="outline"
            className="ml-auto"
            onClick={() =>
              setImporting(found.length > 0 ? "detected" : "paste")
            }
          >
            <FileUpIcon />
            Import
          </Button>
        )}
        {mayManage && env && (
          <Button size="sm" onClick={() => setDialog({ kind: "create" })}>
            <PlusIcon />
            Set variable
          </Button>
        )}
      </div>
      <p className="text-[12.5px] text-muted-foreground">
        A value set for a service beats one set for the project, which beats the
        workspace&apos;s, and a value for one environment beats one for every
        environment.
        {services.length === 0 &&
          " Analyze the repository to set variables for one service."}
      </p>
      {mayManage && env && unset.length > 0 && (
        <Alert>
          <SparklesIcon />
          <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
            <span>
              The analysis found {unset.length}{" "}
              {unset.length === 1 ? "variable" : "variables"}{" "}
              {target.serviceName ?? "this project's services"}{" "}
              {unset.length === 1 ? "reads that isn't" : "read that aren't"} set
              in {env}:{" "}
              <span className="font-mono text-[12px]">
                {unset
                  .slice(0, 4)
                  .map((v) => v.name)
                  .join(", ")}
                {unset.length > 4 ? ` and ${unset.length - 4} more` : ""}
              </span>
            </span>
            <Button
              size="xs"
              variant="outline"
              onClick={() => setImporting("detected")}
            >
              Review them
            </Button>
          </AlertDescription>
        </Alert>
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
      {env && (
        <VariableImport
          open={importing !== null}
          onOpenChange={(open) => !open && setImporting(null)}
          where={where}
          environment={env}
          found={found}
          set={set}
          source={importing ?? "detected"}
          submit={(v) =>
            create
              .mutateAsync({
                path: { id: project.id },
                body: {
                  key: v.key,
                  value: v.value,
                  secret: v.secret,
                  environment: v.environment || undefined,
                  service: service || undefined,
                },
              })
              .then(() => undefined)
          }
          onDone={(count) => {
            toast.success(
              `Set ${count} ${count === 1 ? "variable" : "variables"} ${where}`
            )
            refresh()
          }}
        />
      )}
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
    </div>
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
  if (!resolved.data) return <Skeleton className="h-24 w-full rounded-xl" />
  const rows = resolved.data.variables
  if (rows.length === 0) {
    return (
      <Card className="py-0">
        <p className="px-4 py-3.5 text-[13px] text-muted-foreground">
          Nothing set for {target.serviceName ?? "this project"} in{" "}
          {environment} yet, here or workspace-wide.
        </p>
      </Card>
    )
  }
  return (
    <Card className="py-0">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
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
    </Card>
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
      <TableCell className="font-mono text-[12.5px] font-medium">
        {row.key}
      </TableCell>
      <TableCell className="max-w-xs">
        <Value variable={v} />
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge
            variant={own ? "secondary" : "outline"}
            className="font-normal"
          >
            {layerLabel(v)}
          </Badge>
          <span className="text-[12.5px] text-muted-foreground">
            {reachLabel(v)}
          </span>
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
      <span className="inline-flex items-center gap-1 text-[12.5px] text-muted-foreground">
        <LockIcon className="size-3.5" aria-hidden="true" />
        Secret
      </span>
    )
  }
  return (
    <code
      className={cn(
        "block truncate font-mono text-[12.5px]",
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
