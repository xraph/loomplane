import { useState } from "react"
import { toast } from "sonner"
import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useEnvironmentConnectionsChoose,
  useEnvironmentConnectionsGet,
  useManifestsEdit,
  useManifestsCurrent,
  useProvidersList,
  useProjectsGraph,
} from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  Manifest,
  Provider,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
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
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { cloudLabel } from "@/lib/clouds"
import { useOrganization } from "@/lib/organization"
import {
  compatibleTargets,
  placementRows,
  placementSettings,
  type PlacementFields,
  type PlacementRow,
} from "@/lib/placement"
import { Link } from "@/lib/router"

export function DeploymentTargets({
  projectId,
  manifest,
  environments,
  environment,
  onEnvironment,
  onPendingChange,
}: {
  projectId: string
  manifest: Manifest
  environments: Environment[]
  environment: string
  onEnvironment: (value: string) => void
  onPendingChange?: (pending: boolean) => void
}) {
  const graph = useQuery(useProjectsGraph, { path: { id: projectId } })
  const providers = useQuery(useProvidersList)
  const rows = graph.data?.graph
    ? placementRows(manifest, graph.data.graph)
    : []
  return (
    <Card>
      <CardHeader>
        <CardTitle>Deployment targets</CardTitle>
        <CardDescription>
          Choose where each service runs in {environment}. Save a placement,
          then review a new plan before deploying.
        </CardDescription>
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
          <Empty>
            <EmptyHeader>
              <EmptyTitle>Analyze your repositories first</EmptyTitle>
              <EmptyDescription>
                Service target choices need a successful repository analysis.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : rows.length === 0 ? (
          <Empty>
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
              catalog={providers.data!.providers}
              onPendingChange={onPendingChange}
            />
          ))
        )}
        {providers.data && (
          <EnvironmentAccounts
            projectId={projectId}
            environment={environment}
            manifest={manifest}
            catalog={providers.data.providers}
            onPendingChange={onPendingChange}
          />
        )}
      </CardContent>
    </Card>
  )
}

function TargetRow({
  projectId,
  manifest,
  row,
  environment,
  catalog,
  onPendingChange,
}: {
  projectId: string
  manifest: Manifest
  row: PlacementRow
  environment: string
  catalog: Provider[]
  onPendingChange?: (pending: boolean) => void
}) {
  const { can } = useOrganization()
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const settings = placementSettings(manifest, row, environment)
  const [changes, setChanges] = useState<PlacementFields>({})
  const [error, setError] = useState<string>()
  const [saved, setSaved] = useState(false)
  const value = { ...settings, ...changes }
  const targets = compatibleTargets(row, catalog)
  const selected = catalog.find((p) => p.name === value.provider)
  const offered = targets.some((p) => p.name === value.provider)
  const dirty = Object.keys(changes).length > 0
  const disabled = !can("project:update") || save.isPending
  const change = (patch: PlacementFields) => {
    setChanges((c) => ({ ...c, ...patch }))
    setSaved(false)
  }
  const submit = async () => {
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
  return (
    <div className="grid gap-2 border-b pb-3 last:border-0">
      <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 lg:grid-cols-[minmax(8rem,1fr)_minmax(9rem,1.4fr)_minmax(7rem,1fr)_auto_auto]">
        <div className="col-span-2 min-w-0 lg:col-span-1">
          <p className="truncate text-sm font-medium">{row.name}</p>
          <p className="text-xs text-muted-foreground">
            {row.section === "services"
              ? row.kind || "Source needs review"
              : row.section === "gateways"
                ? "Gateway"
                : row.kind}
          </p>
        </div>
        <Select
          value={value.provider || null}
          onValueChange={(v) => v && change({ provider: v, region: "" })}
        >
          <SelectTrigger
            size="sm"
            className="col-span-2 w-full min-w-0 lg:col-span-1"
            aria-label={`Target for ${row.name}`}
            disabled={disabled || targets.length === 0}
          >
            <SelectValue placeholder="Choose a target">
              {selected?.title ?? value.provider}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            {!offered && value.provider && (
              <SelectItem value={value.provider} disabled>
                {selected?.title ?? value.provider} (unavailable)
              </SelectItem>
            )}
            {targets.map((p) => (
              <SelectItem key={p.name} value={p.name}>
                {p.title}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div>
          <Input
            className="h-8 text-sm"
            aria-label={`Region for ${row.name}`}
            value={value.region}
            placeholder={
              selected?.regions?.length
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
        </div>
        <label className="flex items-center gap-2 text-xs">
          <Switch
            size="sm"
            aria-label={`Enable ${row.name} in ${environment}`}
            checked={value.enabled}
            disabled={disabled}
            onCheckedChange={(enabled) => change({ enabled })}
          />
          Enabled
        </label>
        {can("project:update") && (
          <Button
            size="sm"
            variant="outline"
            aria-label={`Save ${row.name} placement`}
            disabled={disabled || !dirty || (value.enabled && !offered)}
            onClick={() => void submit()}
          >
            Save
          </Button>
        )}
      </div>
      {targets.length === 0 && (
        <p className="text-xs text-muted-foreground">
          No available target supports this{" "}
          {row.section === "resources" ? "resource" : "workload"}. Review its
          source or kind in the manifest.
        </p>
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
  const clouds = new Set<string>()
  for (const section of ["services", "resources", "gateways"] as const) {
    for (const [name, entry] of Object.entries(manifest[section] ?? {})) {
      const settings = placementSettings(
        manifest,
        { section, name, kind: "", entry },
        environment
      )
      const cloud = catalog.find((p) => p.name === settings.provider)?.cloud
      if (settings.enabled && cloud) clouds.add(cloud)
    }
  }
  for (const link of Object.values(manifest.links ?? {})) {
    if (link.environments?.[environment]?.enabled !== false)
      for (const cloud of link.between ?? []) clouds.add(String(cloud))
  }
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
  if (clouds.size === 0) return null
  return (
    <div className="grid gap-2 border-t pt-3">
      <p className="text-sm font-medium">Cloud accounts for {environment}</p>
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
            <div
              key={cloud}
              className="flex min-w-0 flex-wrap items-center gap-2"
            >
              <span className="min-w-24 text-xs text-muted-foreground">
                {cloudLabel(cloud)}
              </span>
              {ready.length === 0 ? (
                <Link
                  to="/integrations"
                  className="text-sm underline underline-offset-4"
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
                    className="w-full min-w-0 sm:w-auto"
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
      <Empty>
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
