import { DomainsIcon, ProjectsIcon, RoutesIcon } from "@/components/icons"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { CloudLoading } from "@/components/cloud-targets/infrastructure-ui"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Profile,
  CloudinfraRoute,
  RouteInputBody,
  RouteOperation,
  Zone,
  ZoneInputBody,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import { Link } from "@/lib/router"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"
import {
  validExternalTTL,
  canUpdateExternalTTL,
} from "@/lib/cloud-infrastructure"
import { useNow } from "@/lib/use-runs"
import { useOrganization } from "@/lib/organization"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  Field,
  Row,
  Rows,
  SetupReads,
  useInfrastructureRefresh,
} from "./infrastructure-ui"
import { MoveEnvironment } from "./pools"
import { supports } from "./servers"

export function Domains({
  cloud,
  projectId,
}: {
  cloud: Profile
  projectId: string
}) {
  const [creating, setCreating] = useState(false)
  const { can } = useOrganization()
  return (
    <section className="grid gap-4" aria-label="Domains">
      {can("organization_cloud:manage") && (
        <Zones onCreate={() => setCreating(true)} />
      )}
      {projectId ? (
        <ProjectRoutes key={projectId} cloud={cloud} projectId={projectId} />
      ) : (
        <CloudEmpty
          kind="notice"
          icon={RoutesIcon}
          title="Choose a project to manage routes"
          action={
            <Button
              nativeButton={false}
              variant="outline"
              render={<Link to="/projects" />}
            >
              Open projects
            </Button>
          }
        >
          Routes belong to a project and environment. Choose a project with
          access to this Cloud above.
        </CloudEmpty>
      )}
      {creating && <ZoneEditor onClose={() => setCreating(false)} />}
    </section>
  )
}
function Zones({ onCreate }: { onCreate: () => void }) {
  const zones = useQuery(api.useDnsZonesList)
  const verify = useMutation(api.useDnsZonesVerify)
  const update = useMutation(api.useDnsZonesUpdate)
  const remove = useMutation(api.useDnsZonesRemove)
  const [editing, setEditing] = useState<Zone | null>(null)
  const [projectIds, setProjects] = useState<string[]>([])
  const projects = useQuery(api.useProjectsList)
  const [error, setError] = useState("")
  const [confirm, setConfirm] = useState("")
  const list = zones.data?.zones.filter((z): z is Zone => !!z) ?? []
  async function act(zone: Zone, kind: string) {
    setError("")
    try {
      const path = { zoneId: zone.id }
      if (kind === "verify") await verify.mutateAsync({ path })
      else if (kind === "remove") {
        if (confirm !== zone.id) {
          setConfirm(zone.id)
          return
        }
        await remove.mutateAsync({ path, body: { revision: zone.revision } })
        setConfirm("")
      } else
        await update.mutateAsync({
          path,
          body: {
            revision: zone.revision,
            projectIds: kind === "save" ? projectIds : zone.projectIds,
            renewProof: kind === "renew",
          },
        })
      setEditing(null)
      await zones.refetch()
    } catch (err) {
      setError(
        `${messageOf(err, "The DNS zone wasn't updated.")}${kind === "verify" ? " Check the published TXT name and proof value at your DNS host, then retry verification. Ownership remains pending." : ""}`
      )
    }
  }
  return (
    <section className="grid gap-3" aria-label="DNS zones">
      <CloudSectionHead
        title="DNS zones"
        description="Zones Loomplane can write records in, and the projects allowed to use each one."
      >
        <Button size="sm" variant="outline" onClick={onCreate}>
          Add DNS zone
        </Button>
      </CloudSectionHead>
      <CloudError message={error} />
      {zones.status === "error" ? (
        <QueryError
          error={zones.error}
          fallback="DNS zones couldn't be loaded."
          retry={zones.refetch}
        />
      ) : !zones.data ? (
        <CloudLoading>Loading DNS zones...</CloudLoading>
      ) : !list.length ? (
        <CloudEmpty
          kind="list"
          icon={DomainsIcon}
          title="No DNS zones"
          action={
            <Button size="sm" variant="outline" onClick={onCreate}>
              Add DNS zone
            </Button>
          }
        >
          Choose managed DNS with a separate provider account, or prove
          ownership of a zone hosted elsewhere.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((zone) => (
            <Row key={zone.id} className="grid gap-2 p-3 text-[13px]">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <p className="font-medium">
                    {zone.name} · {zone.mode}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    DNS account: {zone.connectionId || "External"} ·{" "}
                    {zone.proofName === `_loomplane.${zone.name}` &&
                    zone.proofValue &&
                    zone.verifiedAt &&
                    !zone.verifiedAt.startsWith("0001")
                      ? "ownership verified"
                      : "ownership pending"}{" "}
                    · {zone.projectIds.length} projects
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={verify.isPending}
                    onClick={() => void act(zone, "verify")}
                  >
                    Verify zone
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setEditing(zone)
                      setProjects(zone.projectIds)
                    }}
                  >
                    Zone access
                  </Button>
                  {(!zone.proofName ||
                    !zone.verifiedAt ||
                    zone.verifiedAt.startsWith("0001")) && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => void act(zone, "renew")}
                    >
                      Renew proof
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={remove.isPending}
                    onClick={() => void act(zone, "remove")}
                  >
                    {confirm === zone.id
                      ? "Confirm remove zone"
                      : "Remove zone"}
                  </Button>
                </div>
              </div>
              {zone.proofName && (
                <p className="wrap-anywhere">
                  Publish TXT {zone.proofName} = {zone.proofValue}. Proof
                  expires {zone.proofExpiresAt}. Then verify ownership.
                </p>
              )}
              {zone.mode === "managed" && zone.providerZone?.name && (
                <p className="text-xs text-muted-foreground">
                  Hosted zone: {zone.providerZone.name}. The TXT proof grants
                  only {zone.name} and its children.
                </p>
              )}
              {!!zone.providerZone?.nameservers?.length && (
                <p className="text-xs break-words">
                  Nameservers: {zone.providerZone.nameservers.join(", ")}.
                  Provider hosting alone does not confirm external delegation.
                </p>
              )}
            </Row>
          ))}
        </Rows>
      )}
      {editing && (
        <Dialog open onOpenChange={(o) => !o && setEditing(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Zone project access</DialogTitle>
              <DialogDescription>
                Choose which projects may use {editing.name}.
              </DialogDescription>
            </DialogHeader>
            <SetupReads reads={[{ label: "Projects", query: projects }]}>
              {projects.data?.projects.length === 0 && (
                <CloudEmpty
                  kind="inline"
                  icon={ProjectsIcon}
                  title="No projects available"
                >
                  Create a project before changing zone access.
                </CloudEmpty>
              )}
              {projects.data?.projects.map((p) => (
                <FormField
                  orientation="horizontal"
                  className="flex items-center gap-2 text-sm"
                  key={p.id}
                >
                  <FieldLabel className="contents">
                    <Checkbox
                      checked={projectIds.includes(p.id)}
                      onCheckedChange={(e) =>
                        setProjects(
                          e
                            ? [...projectIds, p.id]
                            : projectIds.filter((id) => id !== p.id)
                        )
                      }
                    />
                    {p.name}
                  </FieldLabel>
                </FormField>
              ))}
            </SetupReads>
            <CloudError message={error} />
            <Button
              disabled={
                update.isPending ||
                projects.status !== "success" ||
                !projects.data?.projects.length
              }
              onClick={() => void act(editing, "save")}
            >
              Save zone access
            </Button>
          </DialogContent>
        </Dialog>
      )}
    </section>
  )
}
function ZoneEditor({ onClose }: { onClose: () => void }) {
  const connections = useQuery(api.useConnectionsList)
  const providers = useQuery(api.useProvidersList)
  const projects = useQuery(api.useProjectsList)
  const create = useMutation(api.useDnsZonesCreate)
  const [input, setInput] = useState<ZoneInputBody>({
    name: "",
    mode: "external",
    projectIds: [],
  })
  const [zone, setZone] = useState<Zone | null>(null)
  const [error, setError] = useState("")
  const accounts =
    connections.data?.connections.filter(
      (c) =>
        c.status === "ready" &&
        supports(providers.data?.providers ?? [], c, "dns:records:upsert")
    ) ?? []
  async function save() {
    setError("")
    try {
      setZone(await create.mutateAsync({ body: input }))
      await api.useDnsZonesList().refetch()
    } catch (err) {
      setError(messageOf(err, "The DNS zone wasn't created."))
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Add DNS zone</DialogTitle>
          <DialogDescription>
            You can use a DNS account independent of your compute provider.
          </DialogDescription>
        </DialogHeader>
        {zone ? (
          <div role="status" className="grid gap-2 text-sm">
            <p>{zone.name} saved. Verify ownership before adding routes.</p>
            {zone.proofName && (
              <p className="wrap-anywhere">
                Publish TXT {zone.proofName} = {zone.proofValue}, then use
                Verify zone.
              </p>
            )}
            <Button variant="outline" onClick={onClose}>
              Close
            </Button>
          </div>
        ) : (
          <>
            <Field label="Zone name">
              <Input
                aria-label="Zone name"
                placeholder="example.com"
                value={input.name}
                onChange={(e) => setInput({ ...input, name: e.target.value })}
              />
            </Field>
            <Field label="DNS mode">
              <Select
                value={input.mode}
                onValueChange={(e) =>
                  setInput({
                    ...input,
                    mode: e ?? "",
                    connectionId: undefined,
                  })
                }
              >
                <SelectTrigger
                  aria-label="DNS mode"
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="external">External DNS</SelectItem>
                  <SelectItem value="managed">Managed DNS</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {input.mode === "managed" && (
              <SetupReads
                reads={[
                  { label: "Providers", query: providers },
                  { label: "Connections", query: connections },
                ]}
              >
                {accounts.length === 0 ? (
                  <CloudEmpty
                    kind="inline"
                    icon={DomainsIcon}
                    title="No eligible DNS accounts"
                    action={
                      <Button
                        nativeButton={false}
                        render={<Link to="/integrations" />}
                      >
                        Open Integrations
                      </Button>
                    }
                  >
                    Connect a ready account that supports DNS record updates.
                  </CloudEmpty>
                ) : (
                  <Field label="DNS account">
                    <Select
                      value={input.connectionId ?? ""}
                      onValueChange={(e) =>
                        setInput({ ...input, connectionId: e ?? "" })
                      }
                    >
                      <SelectTrigger
                        aria-label="DNS account"
                        className={"w-full min-w-0"}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="">Choose DNS account</SelectItem>
                        {accounts.map((c) => (
                          <SelectItem value={c.id} key={c.id}>
                            {c.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                )}
              </SetupReads>
            )}
            <div className="grid gap-2 text-xs">
              <span>Projects allowed to use this zone</span>
              <SetupReads reads={[{ label: "Projects", query: projects }]}>
                {projects.data?.projects.length === 0 && (
                  <CloudEmpty
                    kind="inline"
                    icon={ProjectsIcon}
                    title="Create a project for this zone"
                    action={
                      <Button
                        nativeButton={false}
                        variant="outline"
                        render={<Link to="/projects" />}
                      >
                        Open projects
                      </Button>
                    }
                  >
                    DNS zones require explicit project grants. Create a project,
                    then return to choose its access.
                  </CloudEmpty>
                )}
                {projects.data?.projects.map((p) => (
                  <FormField
                    orientation="horizontal"
                    key={p.id}
                    className="flex items-center gap-2"
                  >
                    <FieldLabel className="contents">
                      <Checkbox
                        checked={input.projectIds.includes(p.id)}
                        onCheckedChange={(e) =>
                          setInput({
                            ...input,
                            projectIds: e
                              ? [...input.projectIds, p.id]
                              : input.projectIds.filter((id) => id !== p.id),
                          })
                        }
                      />
                      {p.name}
                    </FieldLabel>
                  </FormField>
                ))}
              </SetupReads>
            </div>
            <CloudError message={error} />
            <DialogFooter>
              <Button variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button
                disabled={
                  create.isPending ||
                  projects.status !== "success" ||
                  (input.mode === "managed" &&
                    (providers.status !== "success" ||
                      connections.status !== "success")) ||
                  !input.projectIds.length ||
                  !input.name.trim() ||
                  (input.mode === "managed" && !input.connectionId)
                }
                onClick={() => void save()}
              >
                Save DNS zone
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
function ProjectRoutes({
  cloud,
  projectId,
}: {
  cloud: Profile
  projectId: string
}) {
  const routes = useQuery(api.useDomainRoutesList, { path: { projectId } })
  const zones = useQuery(api.useDnsZonesProject, { path: { projectId } })
  const [adding, setAdding] = useState(false)
  const [selected, setSelected] = useState<CloudinfraRoute | null>(null)
  const [ttl, setTTL] = useState<CloudinfraRoute | null>(null)
  const { can } = useOrganization()
  const list =
    routes.data?.routes.filter(
      (r): r is CloudinfraRoute => !!r && r.input.cloudId === cloud.id
    ) ?? []
  useInfrastructureRefresh(
    list.some((route) => ["pending", "waiting"].includes(route.status)),
    routes.refetch
  )
  return (
    <section className="grid gap-3" aria-label="Project routes">
      <CloudSectionHead
        title="Routes"
        count={list.length}
        description="Hostnames and paths in this Cloud that send traffic to the project's services."
      >
        {can("infrastructure:modify") && (
          <Button onClick={() => setAdding(true)}>Add route</Button>
        )}
      </CloudSectionHead>
      {routes.status === "error" ? (
        <QueryError
          error={routes.error}
          fallback="Routes couldn't be loaded."
          retry={routes.refetch}
        />
      ) : !routes.data ? (
        <CloudLoading>Loading project routes...</CloudLoading>
      ) : !list.length ? (
        <CloudEmpty
          kind="list"
          icon={RoutesIcon}
          title="No routes for this project"
          action={
            can("infrastructure:modify") && (
              <Button onClick={() => setAdding(true)}>Add route</Button>
            )
          }
        >
          Review a hostname for a deployed service using a zone granted to this
          project.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((route) => (
            <Row key={route.id}>
              <div className="grid min-w-0 gap-1">
                <p className="font-medium break-all">{route.input.hostname}</p>
                <p className="text-xs text-muted-foreground">
                  {route.input.environmentId} / {route.input.service} ·
                  destination{" "}
                  {route.binding.serverId ||
                    route.binding.address ||
                    route.binding.targetId}{" "}
                  · {route.status}
                </p>
                <RouteStatus route={route} />
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setSelected(route)}
                >
                  Inspect route
                </Button>
                {can("infrastructure:modify") &&
                  canUpdateExternalTTL(
                    route.status,
                    zones.data?.zones.find(
                      (zone) => zone.id === route.input.zoneId
                    )?.mode
                  ) && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setTTL(route)}
                    >
                      Update external TTL
                    </Button>
                  )}
              </div>
            </Row>
          ))}
        </Rows>
      )}
      {adding && (
        <RouteEditor
          cloud={cloud}
          projectId={projectId}
          onClose={() => setAdding(false)}
          onChanged={() => void routes.refetch()}
        />
      )}
      {selected && (
        <RouteDetails
          route={selected}
          onClose={() => setSelected(null)}
          onChanged={() => void routes.refetch()}
        />
      )}
      {ttl && (
        <TTLDialog
          route={ttl}
          onClose={() => setTTL(null)}
          onChanged={() => {
            setTTL(null)
            void routes.refetch()
          }}
        />
      )}
    </section>
  )
}
export function RouteStatus({ route }: { route: CloudinfraRoute }) {
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
      <span>DNS {route.verification.dns ? "verified" : "pending"}</span>
      <span>TLS {route.verification.tls ? "verified" : "pending"}</span>
      <span>Backend {route.verification.backend ? "verified" : "pending"}</span>
      {[
        route.verification.dnsError,
        route.verification.tlsError,
        route.verification.backendError,
      ]
        .filter(Boolean)
        .map((e, i) => (
          <Alert
            variant="destructive"
            className="basis-full text-destructive"
            key={i}
          >
            <AlertDescription>{e}</AlertDescription>
          </Alert>
        ))}
    </div>
  )
}
function TTLDialog({
  route,
  onClose,
  onChanged,
}: {
  route: CloudinfraRoute
  onClose: () => void
  onChanged: () => void
}) {
  const update = useMutation(api.useDomainRoutesUpdateTtl)
  const [ttl, setTTL] = useState(String(route.input.externalTtlSeconds ?? ""))
  const [error, setError] = useState("")
  const [stale, setStale] = useState(false)
  async function save() {
    setError("")
    try {
      await update.mutateAsync({
        path: { projectId: route.projectId, routeId: route.id },
        body: { revision: route.revision, externalTtlSeconds: Number(ttl) },
      })
      onChanged()
    } catch (err) {
      setError(
        messageOf(
          err,
          "The TTL bound wasn't saved. Reload the route and review again."
        )
      )
      if (statusOf(err) === 409) setStale(true)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Update external TTL</DialogTitle>
          <DialogDescription>
            Enter the maximum TTL previously served by your DNS host. Resolver
            success and the new record's requested TTL do not establish this
            bound. Saving invalidates earlier reviews.
          </DialogDescription>
        </DialogHeader>
        <Field label="Maximum previously served TTL (seconds)">
          <Input
            aria-label="Maximum previously served TTL (seconds)"
            type="number"
            min={60}
            max={86400}
            value={ttl}
            onChange={(e) => setTTL(e.target.value)}
          />
        </Field>
        <CloudError message={error} />
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          {stale && (
            <Button variant="outline" onClick={onChanged}>
              Reload route
            </Button>
          )}
          <Button
            disabled={
              stale || update.isPending || !validExternalTTL(Number(ttl))
            }
            onClick={() => void save()}
          >
            Save TTL bound
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
function RouteEditor({
  cloud,
  projectId,
  onClose,
  onChanged,
}: {
  cloud: Profile
  projectId: string
  onClose: () => void
  onChanged: () => void
}) {
  const zones = useQuery(api.useDnsZonesProject, { path: { projectId } })
  const preview = useMutation(api.useDomainRoutesPreview)
  const apply = useMutation(api.useDomainRoutesApply)
  const [input, setInput] = useState<RouteInputBody>({
    cloudId: cloud.id,
    environmentId: "",
    service: "",
    hostname: "",
    port: 80,
    zoneId: "",
  })
  const [operation, setOperation] = useState<RouteOperation | null>(null)
  const [accepted, setAccepted] = useState(false)
  const [error, setError] = useState("")
  const zone = zones.data?.zones.find((z) => z.id === input.zoneId)
  const change = (p: Partial<RouteInputBody>) => {
    setInput({ ...input, ...p })
    setOperation(null)
  }
  async function act() {
    setError("")
    try {
      if (operation) {
        setOperation(
          await apply.mutateAsync({
            path: { projectId, operationId: operation.id },
            body: { review: operation.review },
          })
        )
        setAccepted(true)
        onChanged()
      } else
        setOperation(
          await preview.mutateAsync({ path: { projectId }, body: input })
        )
    } catch (err) {
      setError(
        messageOf(
          err,
          "The route wasn't accepted. Refresh the review after resolving the reported issue."
        )
      )
      setOperation(null)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Add route</DialogTitle>
          <DialogDescription>
            Review the deployed service identity, DNS authority and route before
            applying.
          </DialogDescription>
        </DialogHeader>
        {!accepted && (
          <>
            <MoveEnvironment
              projectId={projectId}
              value={input.environmentId}
              onChange={(environmentId) => change({ environmentId })}
            />
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Route service">
                <Input
                  aria-label="Route service"
                  value={input.service}
                  onChange={(e) => change({ service: e.target.value })}
                />
              </Field>
              <Field label="Service port">
                <Input
                  aria-label="Service port"
                  type="number"
                  min={1}
                  max={65535}
                  value={input.port}
                  onChange={(e) => change({ port: Number(e.target.value) })}
                />
              </Field>
            </div>
            <Field label="Hostname">
              <Input
                aria-label="Route hostname"
                value={input.hostname}
                onChange={(e) => change({ hostname: e.target.value })}
              />
            </Field>
            <Field label="Route zone">
              <Select
                value={input.zoneId}
                onValueChange={(e) =>
                  change({
                    zoneId: e ?? "",
                    externalTtlSeconds: undefined,
                  })
                }
              >
                <SelectTrigger
                  aria-label="Route zone"
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">Choose granted DNS zone</SelectItem>
                  {zones.data?.zones.map((z) => (
                    <SelectItem key={z.id} value={z.id}>
                      {z.name} · {z.mode}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            {zones.status === "error" && (
              <QueryError
                error={zones.error}
                fallback="Granted DNS zones couldn't be loaded."
                retry={zones.refetch}
              />
            )}
            {zone?.mode === "external" && (
              <Field label="Maximum previously served TTL (seconds)">
                <Input
                  aria-label="Maximum previously served TTL (seconds)"
                  type="number"
                  min={60}
                  max={86400}
                  value={input.externalTtlSeconds ?? ""}
                  onChange={(e) =>
                    change({ externalTtlSeconds: Number(e.target.value) })
                  }
                />
                <span className="text-muted-foreground">
                  Read the old TTL from your DNS host before any cross-host
                  change.
                </span>
              </Field>
            )}
          </>
        )}
        {operation && <RouteOperationProgress operation={operation} />}
        <CloudError message={error} />
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {accepted ? "Close" : "Cancel"}
          </Button>
          {!accepted && (
            <Button
              disabled={
                preview.isPending ||
                apply.isPending ||
                !input.hostname ||
                !input.environmentId ||
                !input.service ||
                !input.zoneId ||
                (zone?.mode === "external" &&
                  !validExternalTTL(input.externalTtlSeconds ?? 0))
              }
              onClick={() => void act()}
            >
              {operation ? "Apply reviewed route" : "Review route"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
export function RouteOperationProgress({
  operation,
}: {
  operation: RouteOperation
}) {
  return (
    <div className="grid gap-1 rounded-lg bg-muted p-3 text-xs">
      <p className="font-medium">
        {operation.kind}: {operation.status} · attempt {operation.attempt}
      </p>
      <p className="break-words">{operation.message}</p>
      {operation.desiredRecord?.name && (
        <p className="wrap-anywhere">
          DNS: {operation.desiredRecord.type} {operation.desiredRecord.name} ={" "}
          {operation.desiredRecord.value} · TTL {operation.desiredRecord.ttl}s
        </p>
      )}
      {operation.route?.input.externalTtlSeconds !== undefined && (
        <p>
          Maximum previously served TTL:{" "}
          {operation.route.input.externalTtlSeconds}s
        </p>
      )}
      {operation.notBefore && !operation.notBefore.startsWith("0001") && (
        <p>Retain the previous destination until {operation.notBefore}.</p>
      )}
      {operation.expiresAt && !operation.expiresAt.startsWith("0001") && (
        <p>Review expires: {operation.expiresAt}</p>
      )}
    </div>
  )
}
function RouteDetails({
  route,
  onClose,
  onChanged,
}: {
  route: CloudinfraRoute
  onClose: () => void
  onChanged: () => void
}) {
  const now = useNow()
  const history = useQuery(api.useDomainRoutesHistory, {
    path: { projectId: route.projectId, routeId: route.id },
  })
  const verify = useMutation(api.useDomainRoutesVerify)
  const remove = useMutation(api.useDomainRoutesRemove)
  const retry = useMutation(api.useDomainRoutesRetry)
  const rollback = useMutation(api.useDomainRoutesRollback)
  const cutover = useMutation(api.useDomainRoutesCutover)
  const release = useMutation(api.useDomainRoutesReleaseHandoff)
  const { can } = useOrganization()
  const [error, setError] = useState("")
  const [confirmation, setConfirmation] = useState(false)
  const [stale, setStale] = useState(false)
  const [active, setActive] = useState<RouteOperation | null>(null)
  const list =
    history.data?.operations.filter((o): o is RouteOperation => !!o) ?? []
  const current =
    list.find((operation) => operation.id === active?.id) ?? active ?? list[0]
  const [drainSeconds, setDrainSeconds] = useState(300)
  const busy =
    verify.isPending ||
    remove.isPending ||
    retry.isPending ||
    rollback.isPending ||
    cutover.isPending ||
    release.isPending
  useInfrastructureRefresh(
    list.some((o) => ["pending", "running", "waiting"].includes(o.status)),
    history.refetch
  )
  async function act(kind: string, op?: RouteOperation) {
    setError("")
    try {
      const path = { projectId: route.projectId, routeId: route.id }
      const opPath = {
        projectId: route.projectId,
        operationId:
          kind === "release"
            ? op?.kind === "prepare"
              ? op.id
              : (op?.handoffId ?? "")
            : (op?.id ?? ""),
      }
      let result: RouteOperation
      if (kind === "verify") result = await verify.mutateAsync({ path })
      else if (kind === "remove") {
        if (!confirmation) {
          setConfirmation(true)
          return
        }
        result = await remove.mutateAsync({
          path,
          body: { revision: route.revision },
        })
        setConfirmation(false)
      } else if (kind === "release")
        result = await release.mutateAsync({
          path: opPath,
          body: { drainSeconds },
        })
      else
        result = await (
          kind === "retry" ? retry : kind === "rollback" ? rollback : cutover
        ).mutateAsync({ path: opPath })
      setActive(result)
      await history.refetch()
      onChanged()
    } catch (err) {
      setError(
        messageOf(
          err,
          "Route recovery failed. Reconcile the original attempt before reviewing again."
        )
      )
      if (statusOf(err) === 409) setStale(true)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{route.input.hostname}</DialogTitle>
          <DialogDescription>
            {route.input.environmentId} / {route.input.service} ·{" "}
            {current?.route.status ?? route.status}
          </DialogDescription>
        </DialogHeader>
        <RouteStatus route={current?.route ?? route} />
        {current && <RouteOperationProgress operation={current} />}
        <CloudError message={error} />
        {stale && (
          <Button
            variant="outline"
            onClick={() => {
              onChanged()
              onClose()
            }}
          >
            Refresh route review
          </Button>
        )}
        {can("infrastructure:modify") && (
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => void act("verify")}
            >
              Verify route
            </Button>
            <Button
              variant="destructive"
              disabled={busy || stale}
              onClick={() => void act("remove")}
            >
              {confirmation ? "Confirm remove route" : "Remove route"}
            </Button>
          </div>
        )}
        {list.some((operation) => !!operation.handoffId) && (
          <Field label="Handoff drain interval (seconds)">
            <Input
              aria-label="Handoff drain interval (seconds)"
              type="number"
              min={60}
              max={86400}
              value={drainSeconds}
              onChange={(event) => setDrainSeconds(Number(event.target.value))}
            />
            <span className="text-muted-foreground">
              The server also retains the maximum reviewed old DNS TTL. The
              saved release operation records when cleanup may begin.
            </span>
          </Field>
        )}
        {history.status === "error" ? (
          <QueryError
            error={history.error}
            fallback="Route history couldn't be loaded."
            retry={history.refetch}
          />
        ) : !history.data ? (
          <CloudLoading>Loading route history...</CloudLoading>
        ) : (
          <div className="grid gap-3">
            {list.map((op) => (
              <div key={op.id} className="grid gap-2">
                <RouteOperationProgress operation={op} />
                {can("infrastructure:modify") && (
                  <div className="flex flex-wrap gap-2">
                    {[
                      "failed",
                      "waiting",
                      "dns_pending",
                      "tls_pending",
                      "cleanup_pending",
                    ].includes(op.status) && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => void act("retry", op)}
                      >
                        Retry route operation
                      </Button>
                    )}
                    {op.kind === "prepare" && op.status === "succeeded" && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => void act("cutover", op)}
                      >
                        Cut over prepared route
                      </Button>
                    )}
                    {["cutover", "rollback"].includes(op.kind) &&
                      op.handoffId &&
                      ["succeeded", "waiting", "failed"].includes(
                        op.status
                      ) && (
                        <>
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={busy || op.kind !== "cutover"}
                            onClick={() => void act("rollback", op)}
                          >
                            Roll back handoff
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={
                              busy ||
                              op.status !== "succeeded" ||
                              !Number.isInteger(drainSeconds) ||
                              drainSeconds < 60 ||
                              drainSeconds > 86400 ||
                              Date.parse(op.notBefore) > now
                            }
                            onClick={() => void act("release", op)}
                          >
                            Release retained route
                          </Button>
                        </>
                      )}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
