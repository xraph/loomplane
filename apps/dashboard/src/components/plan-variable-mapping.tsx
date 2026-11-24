import { useState } from "react"
import { toast } from "sonner"
import {
  useProjectVariablesCreate,
  useProjectVariablesList,
  useVariablesCreate,
  useVariablesList,
  useVariablesResolve,
} from "@/client/forge/loomplane/hooks"
import type { Plan } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { partOutputs } from "@/lib/wiring"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  VariableDialog,
  type ResourceOption,
  type VariableInput,
} from "@/components/variable-dialog"
import { useOrganization } from "@/lib/organization"
import { useProjectCan } from "@/lib/project-permissions"

export function planOutputOptions(plan: Plan, scope: string): ResourceOption[] {
  const inventory = new Map(
    plan.actions
      .filter((a) => a.resource?.existingResource)
      // A workspace service has no inventory ID: its own endpoint ID
      // names it across projects.
      .map((a) => [
        a.name,
        a.resource!.existingResource!.resourceId ||
          a.resource!.existingResource!.endpointId,
      ])
  )
  const options = new Map<string, ResourceOption>()
  const add = (resource: string, field: string) => {
    const name = (scope === "workspace" && inventory.get(resource)) || resource
    const value = field === "url" ? name : name + "." + field
    options.set(value, {
      value,
      label: resource + " · " + (field === "url" ? "connection string" : field),
    })
  }
  for (const action of plan.actions) {
    if (action.op === "delete") continue
    // A blocked plan can include the database before any service has a usable link.
    const resource = action.resource
    if (
      resource &&
      !scope.startsWith("service:") &&
      !resource.component &&
      !action.name.includes("--")
    ) {
      if (resource.composition) {
        for (const [i, output] of (
          resource.composition.outputs ?? []
        ).entries()) {
          add(action.name, i === 0 && !output.perConsumer ? "url" : output.name)
        }
      } else if (
        resource.catalog ||
        resource.managed ||
        [
          "postgres",
          "mysql",
          "redis",
          "valkey",
          "mongodb",
          "memcached",
        ].includes(resource.kind)
      ) {
        add(action.name, "url")
        // A database or cache's address has parts a variable can hold.
        if (resource.kind !== "memcached")
          for (const part of partOutputs) add(action.name, part)
      }
    }
    if (
      !action.service ||
      (scope.startsWith("service:") &&
        action.service.service !== scope.slice(8))
    )
      continue
    for (const link of action.service.env ?? []) {
      if (
        link.subject !== "resource" ||
        link.field === "output:CLOUDFLARE_BINDING"
      )
        continue
      const field = (link.field || "url").replace(/^(output|consumer):/, "")
      add(link.from, field)
    }
  }
  return [...options.values()].sort((a, b) => a.label.localeCompare(b.label))
}

export function PlanVariableMapping({
  plan,
  replan,
}: {
  plan: Plan
  replan: () => Promise<void>
}) {
  const can = useProjectCan(plan.projectId)
  const workspace = useOrganization().can("organization_variable:manage")
  const project = can("variable:manage")
  const [open, setOpen] = useState(false)
  const [chosenScope, setScope] = useState<string>()
  const scope = chosenScope ?? (project ? "project" : "workspace")
  const createProject = useMutation(useProjectVariablesCreate)
  const createWorkspace = useMutation(useVariablesCreate)
  const invalidate = useInvalidate()
  if ((!project && !workspace) || !planOutputOptions(plan, "project").length)
    return null
  const services = plan.actions.filter(
    (a) =>
      a.op !== "delete" &&
      a.service?.service &&
      a.service.env?.some(
        (l) =>
          l.subject === "resource" && l.field !== "output:CLOUDFLARE_BINDING"
      )
  )
  const scopes = [
    ...(workspace ? [{ value: "workspace", label: "Workspace" }] : []),
    ...(project
      ? [
          { value: "project", label: "Project" },
          ...services.map((a) => ({
            value: "service:" + a.service!.service,
            label: "Service · " + a.name,
          })),
        ]
      : []),
  ]
  const submit = async (input: VariableInput) => {
    const body = {
      key: input.key,
      value: "",
      resourceOutput: input.resourceOutput,
      environment: input.environment,
    }
    if (scope === "workspace") await createWorkspace.mutateAsync({ body })
    else
      await createProject.mutateAsync({
        path: { id: plan.projectId },
        body: {
          ...body,
          service: scope.startsWith("service:") ? scope.slice(8) : undefined,
        },
      })
    invalidate(useVariablesList)
    invalidate(useProjectVariablesList)
    invalidate(useVariablesResolve)
    toast.success("Resource mapping saved")
    await replan()
  }
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Map variable
      </Button>
      <VariableDialog
        open={open}
        onOpenChange={setOpen}
        mappingOnly
        title="Map a resource to a variable"
        description="A service setting overrides the project. A project setting overrides the workspace."
        defaultEnvironment={plan.environment}
        environments={{ mode: "project", current: plan.environment }}
        resourceOptions={planOutputOptions(plan, scope)}
        submit={submit}
        scopePicker={
          <Field>
            <FieldLabel>Scope</FieldLabel>
            <Select
              items={scopes}
              value={scope}
              onValueChange={(v) => setScope(String(v))}
            >
              <SelectTrigger aria-label="Scope" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {scopes.map((s) => (
                  <SelectItem key={s.value} value={s.value}>
                    {s.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        }
      />
    </>
  )
}
