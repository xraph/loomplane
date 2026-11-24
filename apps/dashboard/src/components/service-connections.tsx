// How a service reads each database or cache it needs, as loomplane.yaml
// says: how it gets in, and the variables it reads it in. Each can be
// changed here, with the same choices Add service offers, and takes effect
// at the service's next deploy.
import { DatabaseIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useCatalogEndpointsList,
  useManifestsCurrent,
  useManifestsEdit,
  useProjectsGraph,
  useProjectVariablesList,
} from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  ActionsCell,
  Cell,
  CloudTable,
  CloudTableRow,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import { CloudSectionHead } from "@/components/cloud-targets/infrastructure-ui"
import { ServiceAccess } from "@/components/service-access"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { VariableWiring, type WiringSample } from "@/components/variable-wiring"
import { envVarFor } from "@/lib/add-service"
import { resourceLabel } from "@/lib/architecture"
import { useProjectCan } from "@/lib/project-permissions"
import { existingResourceId } from "@/lib/replace-resource"
import {
  type Access,
  type AccessChoice,
  accessProblem,
  accessRights,
  type AccessRights,
  choiceOf,
  handSetFor,
  keysOf,
  ownDatabaseName,
  ownUserName,
  suggestWiring,
  wirable,
  wiringBody,
  type WiringChoice,
  wiringProblem,
} from "@/lib/wiring"

/** One need's wiring as loomplane.yaml has it. */
type Wired = {
  access?: string
  database?: string
  env?: Record<string, string>
}

/** A database or cache the service needs, with how it reads it. */
type Need = {
  name: string
  kind: string
  wired: Wired
  /** What a registered workspace service's credentials may do. */
  rights?: AccessRights
  sample?: WiringSample
}

const accessWords: Record<Access, string> = {
  shared: "Same credentials",
  own_database: "Its own database",
  own_user: "Its own user",
}

/** summary words how a service reads one need now. */
function summary(need: Need, fallback: string): string {
  const keys = Object.keys(need.wired.env ?? {})
  const vars = keys.length ? keys.join(", ") : fallback
  const access = (need.wired.access ?? "shared") as Access
  return need.rights
    ? `${accessWords[access] ?? access}${need.wired.database ? ` (${need.wired.database})` : ""}, in ${vars}`
    : `In ${vars}`
}

export function ServiceConnections({
  project,
  service,
}: {
  project: Project
  service: string
}) {
  const can = useProjectCan(project.id)
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const endpoints = useQuery(useCatalogEndpointsList)
  const [editing, setEditing] = useState<string>()
  const manifest = current.data?.manifest
  const entry = manifest?.services?.[service] as
    { needs?: string[]; wiring?: Record<string, Wired> } | undefined
  const needs: Need[] = (entry?.needs ?? []).flatMap((name) => {
    const resource = manifest?.resources?.[name] as
      Record<string, unknown> | undefined
    const kind = typeof resource?.kind === "string" ? resource.kind : ""
    if (!resource || !wirable(kind)) return []
    const id = existingResourceId(resource)
    const endpoint = endpoints.data?.endpoints.find(
      (e) => !!e && e.id === id && (!e.origin || e.origin === "registered")
    )
    return [
      {
        name,
        kind,
        wired: entry?.wiring?.[name] ?? {},
        rights: endpoint ? accessRights(endpoint) : undefined,
        sample: endpoint
          ? {
              host: endpoint.host,
              port: endpoint.port,
              user: endpoint.username,
              database: endpoint.database,
            }
          : undefined,
      },
    ]
  })
  if (!current.data || needs.length === 0) return null
  const open = needs.find((n) => n.name === editing)
  return (
    <section aria-label="How it connects" className="grid gap-2.5">
      <CloudSectionHead
        title="How it connects"
        count={needs.length}
        description={`How ${service} gets into each database or cache it needs, and the variables it reads it in. A change goes out with its next deploy.`}
      />
      <CloudTable
        label="How it connects"
        columns="minmax(0,1fr) minmax(0,1.6fr) 90px"
        head={["Needs", "How", ""]}
        minWidth={520}
      >
        {needs.map((n) => (
          <CloudTableRow key={n.name}>
            <NameCell
              tile={<DatabaseIcon />}
              name={n.name}
              meta={resourceLabel(n.kind)}
            />
            <Cell className="text-[12.5px] text-muted-foreground">
              {summary(
                n,
                envVarFor({ name: n.name, kind: n.kind }, "resources")
              )}
            </Cell>
            <ActionsCell>
              {can("project:update") && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  aria-label={`Change how ${service} reads ${n.name}`}
                  onClick={() => setEditing(n.name)}
                >
                  Change
                </Button>
              )}
            </ActionsCell>
          </CloudTableRow>
        ))}
      </CloudTable>
      {open && (
        <ChangeWiring
          key={open.name}
          project={project}
          service={service}
          need={open}
          onClose={() => setEditing(undefined)}
        />
      )}
    </section>
  )
}

function ChangeWiring({
  project,
  service,
  need,
  onClose,
}: {
  project: Project
  service: string
  need: Need
  onClose: () => void
}) {
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const graph = useQuery(useProjectsGraph, { path: { id: project.id } })
  const variables = useQuery(useProjectVariablesList, {
    path: { id: project.id },
  })
  const fallback = envVarFor({ name: need.name, kind: need.kind }, "resources")
  const [choice, setChoice] = useState<WiringChoice>(() =>
    choiceOf(need.kind, fallback, need.wired.env)
  )
  const [access, setAccess] = useState<AccessChoice>(() => ({
    access: (need.wired.access ?? "shared") as Access,
    database:
      need.wired.database ??
      (need.rights?.engine === "redis" ? "" : ownDatabaseName(service)),
  }))
  const [pending, setPending] = useState(false)
  const reads = (
    graph.data?.graph?.services.find((s) => s.name === service)?.env ?? []
  ).map((e) => e.name)
  const detected = suggestWiring(need.kind, fallback, reads).detected
  const ownUser = project.slug ? ownUserName(project.slug, service) : undefined
  const problem =
    wiringProblem(choice) ||
    (need.rights ? accessProblem(access, need.rights.engine) : "")
  const sample: WiringSample | undefined = (() => {
    const base = need.sample
    if (!base || !need.rights || access.access === "shared") return base
    const database = access.database.trim() || base.database
    const user =
      access.access === "own_user" ? (ownUser ?? "its own user") : base.user
    return { ...base, database, user }
  })()

  const submit = async (reset: boolean) => {
    setPending(true)
    try {
      const body = reset
        ? undefined
        : wiringBody(choice, fallback, need.rights ? access : undefined)
      await save.mutateAsync({
        path: { id: project.id },
        body: { wire: { service, need: need.name, ...(body ?? {}) } },
      })
      invalidate(useManifestsCurrent)
      toast.success(
        `${service} reads ${need.name} the new way from its next deploy.`
      )
      onClose()
    } catch (err) {
      toast.error(messageOf(err, "The change wasn't saved."))
    } finally {
      setPending(false)
    }
  }

  const wiredNow =
    !!need.wired.access || Object.keys(need.wired.env ?? {}).length > 0
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[640px]">
        <DialogHeader>
          <DialogTitle>
            How {service} reads {need.name}
          </DialogTitle>
          <DialogDescription>
            Saved to loomplane.yaml. {service} gets it the new way the next time
            it deploys.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          {need.rights && (
            <ServiceAccess
              service={service}
              need={need.name}
              rights={need.rights}
              value={access}
              ownUser={ownUser}
              onChange={setAccess}
            />
          )}
          <VariableWiring
            service={service}
            need={need.name}
            kind={need.kind}
            fallback={fallback}
            value={choice}
            detected={detected}
            sample={sample}
            handSet={handSetFor(
              service,
              keysOf(choice),
              variables.data?.variables ?? []
            )}
            onChange={setChoice}
          />
        </div>
        <DialogFooter>
          {wiredNow && (
            <Button
              type="button"
              variant="ghost"
              disabled={pending}
              onClick={() => submit(true)}
              className="sm:mr-auto"
            >
              Use the defaults
            </Button>
          )}
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={pending || !!problem}
            onClick={() => submit(false)}
          >
            {pending ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
