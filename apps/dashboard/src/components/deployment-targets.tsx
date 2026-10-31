import { useState } from "react"
import { toast } from "sonner"
import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useEnvironmentConnectionsChoose,
  useEnvironmentCloudsGet,
  useEnvironmentConnectionsGet,
  useManifestsEdit,
  useManifestsCurrent,
  useProvidersList,
  useProjectsGraph,
} from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  Connection,
  Manifest,
  Provider,
  Target,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudSQLApplicationFields } from "@/components/cloudsql-application-fields"
import { cloudSQLApplicationNetworkValid } from "@/lib/cloudsql-network"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { BoxIcon, GlobeIcon, DatabaseIcon } from "lucide-react"
import { CloudGlyph } from "@/components/cloud-glyph"
import { cloudLabel } from "@/lib/clouds"
import { useOrganization } from "@/lib/organization"
import {
  compatibleTargets,
  placementRows,
  placementSettings,
  placementSummary,
  targetConnections,
  type PlacementFields,
  type PlacementRow,
} from "@/lib/placement"
import { Link } from "@/lib/router"
import { useDeploymentDraft } from "@/lib/deployment-writes"

export function DeploymentTargets({
  projectId,
  manifest,
  environments,
  environment,
  onEnvironment,
  onPendingChange,
  setup = false,
}: {
  projectId: string
  setup?: boolean
  manifest: Manifest
  environments: Environment[]
  environment: string
  onEnvironment: (value: string) => void
  onPendingChange?: (pending: boolean) => void
}) {
  const graph = useQuery(useProjectsGraph, { path: { id: projectId } })
  const providers = useQuery(useProvidersList)
  const environmentId = environments.find((e) => e.name === environment)?.id
  const cloud = useQuery(useEnvironmentCloudsGet, {
    path: { id: projectId, environmentId: environmentId ?? "" },
  })
  const cloudTargets = (cloud.data?.assignment?.targets ?? []).filter(
    (t): t is Target => !!t
  )
  const rows = graph.data?.graph
    ? placementRows(manifest, graph.data.graph)
    : []
  return (
    <div className="grid min-w-0 gap-4">
      <Card size="sm">
        <CardHeader>
          <CardTitle>
            {setup ? "Services and resources" : "Deployment targets"}
          </CardTitle>
          <CardDescription>
            Choose where each service runs. Save changes before reviewing your
            deployment.
          </CardDescription>
          {!setup && (
            <CardAction>
              <Select
                value={environment}
                onValueChange={(v) => v && onEnvironment(v)}
              >
                <SelectTrigger size="sm" aria-label="Placement environment">
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
            </CardAction>
          )}
        </CardHeader>
        <CardContent className="grid gap-3">
          {graph.status === "error" || providers.status === "error" ? (
            <QueryError
              error={graph.error ?? providers.error}
              fallback="Deployment targets couldn't be loaded."
              retry={() =>
                Promise.allSettled([graph.refetch(), providers.refetch()])
              }
            />
          ) : !graph.data || !providers.data ? (
            <Skeleton className="h-24" />
          ) : !graph.data.graph ? (
            <Empty preview={false}>
              <EmptyHeader>
                <EmptyTitle>Analyze your repositories first</EmptyTitle>
                <EmptyDescription>
                  Service target choices need a successful repository analysis.
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : rows.length === 0 ? (
            <Empty preview={false}>
              <EmptyHeader>
                <EmptyTitle>No services to place</EmptyTitle>
                <EmptyDescription>
                  Add services or resources to your manifest, then choose their
                  targets here.
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            rows.map((row) => (
              <TargetRow
                key={`${projectId}:${environment}:${row.section}:${row.name}`}
                projectId={projectId}
                manifest={manifest}
                row={row}
                environment={environment}
                environmentId={environmentId}
                catalog={providers.data!.providers}
                cloudTargets={cloudTargets}
                onPendingChange={onPendingChange}
              />
            ))
          )}
        </CardContent>
      </Card>
      {providers.data &&
        placementSummary(manifest, environment, providers.data.providers).clouds
          .length > 0 && (
          <Card size="sm">
            <CardHeader>
              <CardTitle>Accounts</CardTitle>
              <CardDescription className="text-[12.5px]">
                Where {environment}'s services and resources will be installed.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <EnvironmentAccounts
                projectId={projectId}
                environment={environment}
                manifest={manifest}
                catalog={providers.data.providers}
                onPendingChange={onPendingChange}
              />
            </CardContent>
          </Card>
        )}
    </div>
  )
}

function TargetRow({
  projectId,
  manifest,
  row,
  environment,
  environmentId,
  catalog,
  cloudTargets,
  onPendingChange,
}: {
  projectId: string
  manifest: Manifest
  row: PlacementRow
  environment: string
  environmentId?: string
  catalog: Provider[]
  cloudTargets: Target[]
  onPendingChange?: (pending: boolean) => void
}) {
  const { can, session } = useOrganization()
  const save = useMutation(useManifestsEdit)
  const connections = useQuery(useConnectionsList)
  const invalidate = useInvalidate()
  const settings = placementSettings(manifest, row, environment)
  const [changes, setChanges] = useState<PlacementFields>({})
  const [error, setError] = useState<string>()
  const [saved, setSaved] = useState(false)
  const value = { ...settings, ...changes }
  const targets = compatibleTargets(row, catalog)
  const selected = catalog.find((p) => p.name === value.provider)
  const offered = targets.some((p) => p.name === value.provider)
  const [tagsText, setTagsText] = useState<string>()
  const cloudChosen = !!value.cloudTarget || !!value.cloudTags?.length
  // Only services can run on a Cloud target. Offer those whose provider can
  // run this workload; a target whose provider the catalog doesn't know stays.
  const cloudChoices =
    row.section === "services"
      ? cloudTargets.filter((t) => {
          const p = catalog.find((c) => c.name === t.provider)
          return (
            !p || (p.available && p.runs?.some((r) => r.workload === row.kind))
          )
        })
      : []
  const cloudValue = value.cloudTarget
    ? `cloud:${value.cloudTarget}`
    : value.cloudTags?.length
      ? "cloud-tags"
      : undefined
  const cloudLabelText = value.cloudTarget
    ? cloudTargets.find((t) => t.name === value.cloudTarget)?.displayName ||
      value.cloudTarget
    : `Cloud tags: ${value.cloudTags?.join(", ")}`
  const dirty = Object.keys(changes).length > 0
  const disabled = !can("project:update") || save.isPending
  useDeploymentDraft(dirty, onPendingChange)
  const cloudRun = row.section === "services" && value.provider === "cloud-run"
  const route = value.cloudSqlNetwork ?? {
    consumerProject: "",
    network: "",
    subnetwork: "",
  }
  const routeEmpty =
    !route.consumerProject && !route.network && !route.subnetwork
  const routeValid =
    !cloudRun ||
    routeEmpty ||
    cloudSQLApplicationNetworkValid(route, value.region)
  const owned =
    row.section === "services" &&
    (selected?.cloud === "server" || selected?.cloud === "kubernetes")
  const accounts = connections.data?.connections ?? []
  const managedRegistry =
    selected?.cloud === "kubernetes"
      ? accounts.find((c) => c.id === value.target)?.details.registry_hostname
      : undefined
  const buildChange = (patch: NonNullable<PlacementFields["build"]>) =>
    change({ build: { ...value.build, ...patch } })
  const endpointChange = (patch: NonNullable<PlacementFields["endpoint"]>) =>
    change({ endpoint: { ...value.endpoint, ...patch } })
  const change = (patch: PlacementFields) => {
    setChanges((c) => ({ ...c, ...patch }))
    setSaved(false)
  }
  const submit = async () => {
    if (!routeValid) return
    setError(undefined)
    onPendingChange?.(true)
    try {
      await save.mutateAsync({
        path: { id: projectId },
        body: {
          placement: {
            section: row.section,
            name: row.name,
            environment,
            ...changes,
          },
        },
      })
      setSaved(true)
      setChanges({})
      toast.success(`Saved ${row.name} placement for ${environment}`)
      invalidate(useManifestsCurrent)
    } catch (err) {
      setError(messageOf(err, "The placement wasn't saved."))
    } finally {
      onPendingChange?.(false)
    }
  }
  const manageCloud = () => {
    window.localStorage.setItem(
      `loomplane.clouds.project.${session.orgId}`,
      projectId
    )
    if (environmentId)
      window.localStorage.setItem(
        `loomplane.clouds.environment.${session.orgId}.${projectId}`,
        environmentId
      )
  }
  return (
    <section
      aria-label={`${row.name} configuration`}
      className="grid min-w-0 gap-3 border-b pb-3 last:border-0 last:pb-0"
    >
      <div className="flex items-center gap-2.5">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
          {row.section === "resources" ? (
            <DatabaseIcon className="size-4" />
          ) : row.kind === "static-site" || row.kind === "web-app" ? (
            <GlobeIcon className="size-4" />
          ) : (
            <BoxIcon className="size-4" />
          )}
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-medium break-words">{row.name}</p>
          <p className="font-mono text-xs break-all text-muted-foreground">
            {row.entry.source ?? row.kind}
          </p>
        </div>
        <Switch
          size="sm"
          aria-label={`Enable ${row.name} in ${environment}`}
          checked={value.enabled}
          disabled={disabled}
          onCheckedChange={(enabled) => change({ enabled })}
        />
        {can("project:update") && dirty && (
          <Button
            size="sm"
            variant="outline"

            aria-label={`Save ${row.name} placement`}
            disabled={
              disabled ||
              !routeValid ||
              (value.enabled && !offered && !cloudChosen)
            }
            onClick={() => void submit()}
          >
            Save
          </Button>
        )}
      </div>
      {value.enabled && (
        <>
          <div className="grid min-w-0 gap-2 sm:grid-cols-2">
            <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
              Infrastructure
              <Select
                value={cloudValue ?? (value.provider || null)}
                onValueChange={(v) =>
                  v &&
                  (v.startsWith("cloud:")
                    ? change({
                        cloudTarget: v.slice("cloud:".length),
                        cloudTags: [],
                        provider: "",
                        region: "",
                        target: "",
                        endpoint: {},
                        ...(value.cloudSqlNetwork
                          ? {
                              cloudSqlNetwork: {
                                consumerProject: "",
                                network: "",
                                subnetwork: "",
                              },
                            }
                          : {}),
                      })
                    : v !== "cloud-tags" &&
                      change({
                        provider: v,
                        ...(cloudChosen
                          ? { cloudTarget: "", cloudTags: [] }
                          : {}),
                        region: "",
                        ...(value.cloudSqlNetwork
                          ? {
                              cloudSqlNetwork: {
                                consumerProject: "",
                                network: "",
                                subnetwork: "",
                              },
                            }
                          : {}),
                        ...(owned || v === "server" || v === "kubernetes"
                          ? { target: "", endpoint: {} }
                          : {}),
                        ...(owned && v !== "server" && v !== "kubernetes"
                          ? { build: { context: value.build?.context } }
                          : {}),
                      }))
                }
              >
                <SelectTrigger
                  size="sm"
                  className="w-full min-w-0"
                  aria-label={`Target for ${row.name}`}
                  disabled={
                    disabled ||
                    (targets.length === 0 && cloudChoices.length === 0)
                  }
                >
                  <SelectValue placeholder="Choose a target">
                    {cloudChosen
                      ? cloudLabelText
                      : (selected?.title ?? value.provider)}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {!offered && value.provider && (
                    <SelectItem value={value.provider} disabled>
                      {selected?.title ?? value.provider} (unavailable)
                    </SelectItem>
                  )}
                  {value.cloudTags?.length && !value.cloudTarget ? (
                    <SelectItem value="cloud-tags" disabled>
                      {cloudLabelText}
                    </SelectItem>
                  ) : null}
                  {cloudChoices.length > 0 && (
                    <SelectGroup>
                      <SelectLabel>Cloud targets</SelectLabel>
                      {cloudChoices.map((t) => (
                        <SelectItem key={t.id} value={`cloud:${t.name}`}>
                          {t.displayName || t.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  )}
                  {targets.length > 0 && (
                    <SelectGroup>
                      {cloudChoices.length > 0 && (
                        <SelectLabel>Providers</SelectLabel>
                      )}
                      {targets.map((p) => (
                        <SelectItem key={p.name} value={p.name}>
                          <span aria-hidden="true">
                            <CloudGlyph cloud={p.cloud} />
                          </span>
                          {p.title}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  )}
                </SelectContent>
              </Select>
            </label>
            <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
              Region
              <Input
                className="h-8 text-[13px]"
                aria-label={`Region for ${row.name}`}
                value={value.region}
                placeholder={
                  cloudChosen
                    ? "Set by Cloud target"
                    : selected?.regions?.length
                      ? "Automatic region"
                      : "Provider default"
                }
                list={`regions-${row.section}-${row.name}`}
                disabled={disabled || !selected?.regions?.length}
                onChange={(e) => change({ region: e.target.value })}
              />
              <datalist id={`regions-${row.section}-${row.name}`}>
                {selected?.regions?.map((r) => (
                  <option key={r} value={r} />
                ))}
              </datalist>
            </label>
          </div>
          {row.section === "services" && (
            <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
              Cloud tags
              <Input
                className="h-8 text-[13px]"
                aria-label={`Cloud tags for ${row.name}`}
                value={tagsText ?? (value.cloudTags ?? []).join(", ")}
                placeholder="e.g. prod, eu (runs on a Cloud target with these tags)"
                list={`tags-${row.name}`}
                disabled={disabled || !!value.cloudTarget}
                onChange={(e) => {
                  setTagsText(e.target.value)
                  const tags = e.target.value
                    .split(",")
                    .map((t) => t.trim())
                    .filter(Boolean)
                  change(
                    tags.length
                      ? {
                          cloudTags: tags,
                          cloudTarget: "",
                          provider: "",
                          region: "",
                          target: "",
                          endpoint: {},
                        }
                      : { cloudTags: [] }
                  )
                }}
              />
              <datalist id={`tags-${row.name}`}>
                {[...new Set(cloudTargets.flatMap((t) => t.tags ?? []))].map(
                  (t) => (
                    <option key={t} value={t} />
                  )
                )}
              </datalist>
            </label>
          )}
          {row.section === "services" && cloudTargets.length === 0 && (
            <p className="text-xs text-muted-foreground">
              No Cloud is assigned to this environment, so no Cloud targets are
              listed.{" "}
              <Link
                to="/clouds"
                onClick={manageCloud}
                className="underline underline-offset-4"
              >
                Assign a Cloud
              </Link>
            </p>
          )}
          {cloudChosen && (
            <Link
              to="/clouds"
              onClick={manageCloud}
              className="w-fit text-xs underline underline-offset-4"
            >
              Manage Cloud target
            </Link>
          )}
          {cloudRun && (
            <>
              <CloudSQLApplicationFields
                name={row.name}
                value={route}
                disabled={disabled}
                onChange={(cloudSqlNetwork) => change({ cloudSqlNetwork })}
              />
              {!routeValid && (
                <p role="alert" className="text-xs text-destructive">
                  Choose a canonical VPC and subnet in the same host project and
                  service region, or clear both fields to restore inheritance.
                </p>
              )}
            </>
          )}
          {owned && (
            <div className="grid min-w-0 gap-2 sm:grid-cols-2 xl:grid-cols-3">
              <ConnectionChoice
                label={`Infrastructure for ${row.name}`}
                value={value.target}
                connections={targetConnections(accounts, selected!.cloud)}
                disabled={disabled}
                automatic="Environment default"
                onChange={(target) => change({ target, endpoint: {} })}
              />
              <ConnectionChoice
                label={`Builder for ${row.name}`}
                value={value.build?.builder}
                connections={targetConnections(accounts, "server")}
                disabled={disabled || !!value.build?.image}
                automatic={
                  selected?.cloud === "server"
                    ? "Use target server"
                    : managedRegistry
                      ? "Build in cluster"
                      : "Cluster default or SSH builder"
                }
                onChange={(builder) => buildChange({ builder })}
              />
              <ConnectionChoice
                label={`Registry for ${row.name}`}
                value={value.build?.registry}
                connections={accounts.filter(
                  (c) =>
                    c.cloud === "registry" &&
                    (c.status === "ready" ||
                      (!!value.build?.image && c.details.pull === "ready"))
                )}
                disabled={disabled}
                automatic={
                  managedRegistry &&
                  !value.build?.builder &&
                  !value.build?.image
                    ? "Managed cluster registry"
                    : "No registry selected"
                }
                onChange={(registry) => buildChange({ registry })}
              />
              {managedRegistry &&
                !value.build?.builder &&
                !value.build?.registry &&
                !value.build?.image && (
                  <p className="text-xs text-muted-foreground sm:col-span-2 xl:col-span-3">
                    Builds run in this cluster and publish to {managedRegistry}.
                    Loomplane manages the registry and image pull secrets.
                  </p>
                )}
              <label className="grid gap-1.5 text-[12.5px] font-medium">
                Build context
                <Input
                  className="h-8 text-xs"
                  aria-label={`Build context for ${row.name}`}
                  value={value.build?.context ?? ""}
                  placeholder="Repository root"
                  disabled={disabled}
                  onChange={(e) => buildChange({ context: e.target.value })}
                />
              </label>
              <label className="grid gap-1.5 text-[12.5px] font-medium sm:col-span-2">
                Existing image (optional)
                <Input
                  className="h-8 min-w-0 font-mono text-xs"
                  aria-label={`Image for ${row.name}`}
                  value={value.build?.image ?? ""}
                  placeholder="registry/repository@sha256:..."
                  disabled={disabled}
                  onChange={(e) => buildChange({ image: e.target.value })}
                />
              </label>
              {row.kind !== "worker" && (
                <>
                  <label className="grid gap-1.5 text-[12.5px] font-medium">
                    Readiness path
                    <Input
                      className="h-8 text-xs"
                      aria-label={`Readiness path for ${row.name}`}
                      value={value.healthPath ?? ""}
                      placeholder="/ready"
                      disabled={disabled}
                      onChange={(e) => change({ healthPath: e.target.value })}
                    />
                  </label>
                  {selected?.cloud === "server" ? (
                    <label className="grid gap-1.5 text-[12.5px] font-medium">
                      Published host port
                      <Input
                        className="h-8 text-xs"
                        aria-label={`Host port for ${row.name}`}
                        type="number"
                        min={1}
                        max={65535}
                        value={value.endpoint?.hostPort ?? ""}
                        placeholder="Private until published"
                        disabled={disabled}
                        onChange={(e) =>
                          endpointChange({
                            hostPort: e.target.value
                              ? Number(e.target.value)
                              : undefined,
                          })
                        }
                      />
                    </label>
                  ) : (
                    <>
                      <label className="grid gap-1.5 text-[12.5px] font-medium">
                        Public hostname
                        <Input
                          className="h-8 text-xs"
                          aria-label={`Hostname for ${row.name}`}
                          value={value.endpoint?.hostname ?? ""}
                          placeholder="api.example.com"
                          disabled={disabled}
                          onChange={(e) =>
                            endpointChange({ hostname: e.target.value })
                          }
                        />
                      </label>
                      <label className="grid gap-1.5 text-[12.5px] font-medium">
                        Ingress class
                        <Input
                          className="h-8 text-xs"
                          aria-label={`Ingress class for ${row.name}`}
                          value={value.endpoint?.ingressClass ?? ""}
                          placeholder="Cluster default"
                          disabled={disabled}
                          onChange={(e) =>
                            endpointChange({ ingressClass: e.target.value })
                          }
                        />
                      </label>
                      <label className="grid gap-1.5 text-[12.5px] font-medium">
                        TLS secret
                        <Input
                          className="h-8 text-xs"
                          aria-label={`TLS secret for ${row.name}`}
                          value={value.endpoint?.tlsSecret ?? ""}
                          placeholder="Existing namespace secret"
                          disabled={disabled}
                          onChange={(e) =>
                            endpointChange({ tlsSecret: e.target.value })
                          }
                        />
                      </label>
                    </>
                  )}
                </>
              )}
              {selected?.logNote && (
                <p className="text-xs text-muted-foreground sm:col-span-2 xl:col-span-3">
                  {selected.logNote}
                </p>
              )}
              {connections.status === "error" && (
                <QueryError
                  error={connections.error}
                  fallback="Infrastructure choices couldn't be loaded."
                  retry={connections.refetch}
                />
              )}
            </div>
          )}
          {targets.length === 0 && cloudChoices.length === 0 && (
            <p className="text-xs text-muted-foreground">
              No available target supports this{" "}
              {row.section === "resources" ? "resource" : "workload"}. Review
              its source or kind in the manifest.
            </p>
          )}
        </>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {saved && (
        <p role="status" className="text-xs text-muted-foreground">
          Placement saved. Create a new plan to review the change.
        </p>
      )}
    </section>
  )
}

function ConnectionChoice({
  label,
  value,
  connections,
  disabled,
  automatic,
  onChange,
}: {
  label: string
  value?: string
  connections: Connection[]
  disabled: boolean
  automatic: string
  onChange: (value: string) => void
}) {
  const found = connections.find((c) => c.id === value)
  return (
    <div className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
      <span>{label}</span>
      <Select
        value={value || "automatic"}
        onValueChange={(v) => v && onChange(v === "automatic" ? "" : v)}
      >
        <SelectTrigger
          size="sm"
          className="w-full min-w-0"
          aria-label={label}
          disabled={disabled}
        >
          <SelectValue>
            {found?.name ?? (value ? "Connection unavailable" : automatic)}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="automatic">{automatic}</SelectItem>
          {connections.map((c) => (
            <SelectItem key={c.id} value={c.id}>
              {c.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

/** Account choices are persistent environment settings shared with the planner. */
export function EnvironmentAccounts({
  projectId,
  environment,
  manifest,
  catalog,
  onPendingChange,
}: {
  projectId: string
  environment: string
  manifest: Manifest
  catalog: Provider[]
  onPendingChange?: (pending: boolean) => void
}) {
  const list = useQuery(useConnectionsList)
  const chosen = useQuery(useEnvironmentConnectionsGet, {
    path: { id: projectId, environment },
  })
  const choose = useMutation(useEnvironmentConnectionsChoose)
  const { can } = useOrganization()
  const [error, setError] = useState<string>()
  const { clouds } = placementSummary(manifest, environment, catalog)
  const pick = async (cloud: string, id: string) => {
    setError(undefined)
    onPendingChange?.(true)
    try {
      await choose.mutateAsync({
        path: { id: projectId, environment, cloud },
        body: { connectionId: id },
      })
      await chosen.refetch()
    } catch (err) {
      setError(messageOf(err, "The cloud account wasn't chosen."))
    } finally {
      onPendingChange?.(false)
    }
  }
  if (clouds.length === 0) return null
  return (
    <div className="grid gap-2 border-t pt-3">
      {list.status === "error" || chosen.status === "error" ? (
        <QueryError
          error={list.error ?? chosen.error}
          fallback="Cloud accounts couldn't be loaded."
          retry={() => Promise.allSettled([list.refetch(), chosen.refetch()])}
        />
      ) : !list.data || !chosen.data ? (
        <Skeleton className="h-8" />
      ) : (
        [...clouds].sort().map((cloud) => {
          const ready = list.data!.connections.filter(
            (c) => c.cloud === cloud && c.status === "ready"
          )
          const id = String(
            chosen.data!.connections[cloud] ??
              (ready.length === 1 ? ready[0].id : "")
          )
          const selected = ready.find((c) => c.id === id)
          return (
            <div key={cloud} className="grid min-w-0 gap-1.5">
              <span className="min-w-24 text-xs text-muted-foreground">
                {cloudLabel(cloud)}
              </span>
              {ready.length === 0 ? (
                <Link
                  to="/integrations"
                  className="text-[13px] underline underline-offset-4"
                >
                  Connect {cloudLabel(cloud)}
                </Link>
              ) : (
                <Select
                  value={selected?.id ?? null}
                  onValueChange={(v) => v && void pick(cloud, v)}
                >
                  <SelectTrigger
                    size="sm"
                    className="w-full min-w-0"
                    aria-label={`Account for ${cloudLabel(cloud)} in ${environment}`}
                    disabled={!can("cloud_account:manage") || choose.isPending}
                  >
                    <SelectValue
                      placeholder={
                        id
                          ? "Selected account isn't ready"
                          : "Choose an account"
                      }
                    >
                      {selected
                        ? `${selected.name} · ${selected.account}`
                        : undefined}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {ready.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name} · {c.account}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>
          )
        })
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </div>
  )
}

export function DeploymentAccounts({
  projectId,
  environment,
  onPendingChange,
}: {
  projectId: string
  environment: string
  onPendingChange?: (pending: boolean) => void
}) {
  const current = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const catalog = useQuery(useProvidersList)
  if (
    (current.status === "error" && statusOf(current.error) !== 404) ||
    catalog.status === "error"
  ) {
    return (
      <QueryError
        error={catalog.error ?? current.error}
        fallback="Deployment settings couldn't be loaded."
        retry={() => Promise.allSettled([current.refetch(), catalog.refetch()])}
      />
    )
  }
  if (current.status === "error" && statusOf(current.error) === 404) {
    return (
      <Empty preview={false}>
        <EmptyHeader>
          <EmptyTitle>Save a manifest first</EmptyTitle>
          <EmptyDescription>
            Open your project's Deploy tab to save the draft and choose service
            targets before planning.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }
  if (!current.data?.manifest || !catalog.data)
    return <Skeleton className="h-8" />
  return (
    <EnvironmentAccounts
      key={environment}
      projectId={projectId}
      environment={environment}
      manifest={current.data.manifest}
      catalog={catalog.data.providers}
      onPendingChange={onPendingChange}
    />
  )
}
