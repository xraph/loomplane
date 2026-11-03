// Where a service runs: the side panel that changes one service's placement
// in one environment, the hook that saves placement edits, and the "Runs on"
// cell the deploy screen and the project's Deployments tab share.
import { ChevronDownIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionsList,
  useEnvironmentCloudsGet,
  useEnvironmentConnectionsChoose,
  useEnvironmentConnectionsGet,
  useManifestsCurrent,
  useManifestsEdit,
  useManifestsSave,
  useProvidersList,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Environment,
  Manifest,
  Provider,
  Target,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { CloudCallout, Cell } from "@/components/cloud-targets/cloud-page"
import { CloudSQLApplicationFields } from "@/components/cloudsql-application-fields"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { adoptServiceTarget } from "@/lib/cloud-targets"
import { cloudSQLApplicationNetworkValid } from "@/lib/cloudsql-network"
import { cloudLabel } from "@/lib/clouds"
import { useDeploymentDraft } from "@/lib/deployment-writes"
import { useOrganization } from "@/lib/organization"
import {
  applyPlacement,
  cloudChoice,
  cloudOwnedFields,
  compatibleTargets,
  placementSettings,
  targetConnections,
  type PlacementFields,
  type PlacementRow,
  type RunsOn,
} from "@/lib/placement"
import { Link } from "@/lib/router"

/**
 * PendingPlacement is one row's unsaved change: its placement fields and,
 * when the panel picked one, the account its cloud deploys with.
 */
export type PendingPlacement = {
  changes: PlacementFields
  account?: { cloud: string; id: string }
}

/** PlacementEdit is a pending change with the row and environment it's for. */
export type PlacementEdit = PendingPlacement & {
  row: PlacementRow
  environment: string
}

/**
 * usePlacementSave saves placement edits. One plain edit goes through the
 * API's placement edit, which keeps the YAML's comments; several, or one that
 * picks a Cloud target, land together as one new revision of loomplane.yaml.
 * Account choices are environment settings and are saved after.
 */
export function usePlacementSave(projectId: string) {
  const edit = useMutation(useManifestsEdit)
  const save = useMutation(useManifestsSave)
  const choose = useMutation(useEnvironmentConnectionsChoose)
  const invalidate = useInvalidate()
  const run = async (
    edits: PlacementEdit[],
    base: { manifest: Manifest; revision: number },
    environments: Environment[]
  ) => {
    const placed = edits.filter((e) => Object.keys(e.changes).length > 0)
    if (placed.length === 1 && !cloudChoice(placed[0].changes)) {
      const e = placed[0]
      await edit.mutateAsync({
        path: { id: projectId },
        body: {
          placement: {
            section: e.row.section,
            name: e.row.name,
            environment: e.environment,
            ...e.changes,
          },
        },
      })
    } else if (placed.length > 0) {
      let next = base.manifest
      for (const e of placed) next = placedManifest(next, environments, e)
      await save.mutateAsync({
        path: { id: projectId },
        body: { manifest: next, baseRevision: base.revision },
      })
    }
    for (const e of edits) {
      if (!e.account) continue
      await choose.mutateAsync({
        path: {
          id: projectId,
          environment: e.environment,
          cloud: e.account.cloud,
        },
        body: { connectionId: e.account.id },
      })
    }
    if (placed.length) invalidate(useManifestsCurrent)
    if (edits.some((e) => e.account)) invalidate(useEnvironmentConnectionsGet)
  }
  return {
    run,
    pending: edit.isPending || save.isPending || choose.isPending,
  }
}

// placedManifest applies one edit in memory. A Cloud target choice rewrites
// the service across environments first, as adopting one on its own does.
function placedManifest(
  m: Manifest,
  environments: Environment[],
  e: PlacementEdit
): Manifest {
  const choice = cloudChoice(e.changes)
  if (!choice || e.row.section !== "services")
    return applyPlacement(m, e.row, e.environment, e.changes)
  const adopted = adoptServiceTarget(
    m,
    environments,
    e.environment,
    e.row.name,
    choice
  )
  const rest = { ...e.changes }
  for (const key of cloudOwnedFields) delete rest[key]
  return applyPlacement(adopted, e.row, e.environment, rest)
}

/**
 * usePlacementContext loads what a "Runs on" cell names: the provider
 * catalog, the environment's Cloud targets and the organization's accounts.
 */
export function usePlacementContext(projectId: string, environmentId?: string) {
  const providers = useQuery(useProvidersList)
  const connections = useQuery(useConnectionsList)
  const cloud = useQuery(useEnvironmentCloudsGet, {
    path: { id: projectId, environmentId: environmentId ?? "" },
  })
  return {
    providers,
    catalog: providers.data?.providers ?? [],
    connections: connections.data?.connections ?? [],
    cloudTargets: (cloud.data?.assignment?.targets ?? []).filter(
      (t): t is Target => !!t
    ),
  }
}

/** RunsOnCell is a table cell naming where a row runs, and where it moves. */
export function RunsOnCell({
  runs,
  moves,
}: {
  runs: RunsOn
  /** moves names where a pending change puts it. */
  moves?: RunsOn
}) {
  return (
    <Cell className="flex min-w-0 items-center gap-2.5">
      <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted">
        {runs.cloud ? (
          <CloudGlyph cloud={runs.cloud} />
        ) : (
          <span className="size-1.5 rounded-full bg-muted-foreground/50" />
        )}
      </span>
      <span className="grid min-w-0 gap-px">
        <span className="truncate font-medium">{runs.name}</span>
        {runs.where && (
          <span className="truncate text-xs text-muted-foreground">
            {runs.where}
          </span>
        )}
        {moves && (
          <StatusPill tone="waiting" className="mt-0.5">
            Moves to {moves.name}
          </StatusPill>
        )}
      </span>
    </Cell>
  )
}

/**
 * PlacementPanel changes where one service, resource or gateway runs in one
 * environment. On the deploy screen it hands its change back to wait for
 * "Review plan"; on the project's Deployments tab it saves at once.
 */
export function PlacementPanel({
  projectId,
  manifest,
  revision,
  row,
  environment,
  environments,
  mode,
  pending,
  onUse,
  onDiscard,
  onClose,
  onPendingChange,
}: {
  projectId: string
  manifest: Manifest
  revision: number
  /** row is the one being changed; null closes the panel. */
  row: PlacementRow | null
  environment: string
  environments: Environment[]
  mode: "pending" | "save"
  /** pending is the change already waiting for this row, on the deploy screen. */
  pending?: PendingPlacement
  onUse?: (change: PendingPlacement) => void
  /** onDiscard drops the pending change, when there is one. */
  onDiscard?: () => void
  onClose: () => void
  onPendingChange?: (pending: boolean) => void
}) {
  return (
    <Sheet
      open={row !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <SheetContent className="w-full gap-0 sm:max-w-md">
        {row && (
          <PlacementForm
            key={`${environment}:${row.section}:${row.name}`}
            projectId={projectId}
            manifest={manifest}
            revision={revision}
            row={row}
            environment={environment}
            environments={environments}
            mode={mode}
            pending={pending}
            onUse={onUse}
            onDiscard={onDiscard}
            onClose={onClose}
            onPendingChange={onPendingChange}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function PlacementForm({
  projectId,
  manifest,
  revision,
  row,
  environment,
  environments,
  mode,
  pending,
  onUse,
  onDiscard,
  onClose,
  onPendingChange,
}: {
  projectId: string
  manifest: Manifest
  revision: number
  row: PlacementRow
  environment: string
  environments: Environment[]
  mode: "pending" | "save"
  pending?: PendingPlacement
  onUse?: (change: PendingPlacement) => void
  onDiscard?: () => void
  onClose: () => void
  onPendingChange?: (pending: boolean) => void
}) {
  const environmentId = environments.find((e) => e.name === environment)?.id
  const { providers, cloudTargets } = usePlacementContext(
    projectId,
    environmentId
  )
  return (
    <>
      <SheetHeader className="border-b">
        <SheetTitle>
          Where {row.name} runs in {environment}
        </SheetTitle>
        <SheetDescription>
          {mode === "pending"
            ? `Becomes ${environment}'s standing setting when you review the plan. Nothing saves before that.`
            : `Saving makes this ${environment}'s standing setting. Nothing moves until the next deploy.`}
        </SheetDescription>
      </SheetHeader>
      {providers.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={providers.error}
            fallback="Targets couldn't be loaded."
            retry={providers.refetch}
          />
        </div>
      ) : !providers.data ? (
        <div className="p-4">
          <Skeleton className="h-48 rounded-xl" />
        </div>
      ) : (
        <PlacementChoices
          projectId={projectId}
          manifest={manifest}
          revision={revision}
          row={row}
          environment={environment}
          environmentId={environmentId}
          environments={environments}
          catalog={providers.data.providers}
          cloudTargets={cloudTargets}
          mode={mode}
          pending={pending}
          onUse={onUse}
          onDiscard={onDiscard}
          onClose={onClose}
          onPendingChange={onPendingChange}
        />
      )}
    </>
  )
}

const noRoute = { consumerProject: "", network: "", subnetwork: "" }

function PlacementChoices({
  projectId,
  manifest,
  revision,
  row,
  environment,
  environmentId,
  environments,
  catalog,
  cloudTargets,
  mode,
  pending,
  onUse,
  onDiscard,
  onClose,
  onPendingChange,
}: {
  projectId: string
  manifest: Manifest
  revision: number
  row: PlacementRow
  environment: string
  environmentId?: string
  environments: Environment[]
  catalog: Provider[]
  cloudTargets: Target[]
  mode: "pending" | "save"
  pending?: PendingPlacement
  onUse?: (change: PendingPlacement) => void
  onDiscard?: () => void
  onClose: () => void
  onPendingChange?: (pending: boolean) => void
}) {
  const { can, session } = useOrganization()
  const placement = usePlacementSave(projectId)
  const connections = useQuery(useConnectionsList)
  const settings = placementSettings(manifest, row, environment)
  const [changes, setChanges] = useState<PlacementFields>(
    pending?.changes ?? {}
  )
  const [account, setAccount] = useState(pending?.account)
  const [error, setError] = useState<string>()
  const [tagsText, setTagsText] = useState<string>()
  const value = { ...settings, ...changes }
  const targets = compatibleTargets(row, catalog)
  const selected = catalog.find((p) => p.name === value.provider)
  const offered = targets.some((p) => p.name === value.provider)
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
    : `Tagged ${value.cloudTags?.join(", ")}`
  const dirty = Object.keys(changes).length > 0 || !!account
  const disabled = !can("project:update") || placement.pending
  // A panel that saves at once holds back plans until it's saved or closed.
  useDeploymentDraft(mode === "save" && dirty, onPendingChange)
  const cloudRun = row.section === "services" && value.provider === "cloud-run"
  const route = value.cloudSqlNetwork ?? noRoute
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
  // Owned clouds pick an account per service; the rest per environment.
  const accountCloud =
    value.enabled && !cloudChosen && selected && !owned
      ? selected.cloud
      : undefined
  const change = (patch: PlacementFields) =>
    setChanges((c) => ({ ...c, ...patch }))
  const buildChange = (patch: NonNullable<PlacementFields["build"]>) =>
    change({ build: { ...value.build, ...patch } })
  const endpointChange = (patch: NonNullable<PlacementFields["endpoint"]>) =>
    change({ endpoint: { ...value.endpoint, ...patch } })
  const blocked =
    disabled ||
    !dirty ||
    !routeValid ||
    (value.enabled && !offered && !cloudChosen)

  const submit = async () => {
    if (blocked) return
    const edit = { changes, ...(account ? { account } : {}) }
    if (mode === "pending") {
      onUse?.(edit)
      onClose()
      return
    }
    setError(undefined)
    onPendingChange?.(true)
    try {
      await placement.run(
        [{ ...edit, row, environment }],
        { manifest, revision },
        environments
      )
      setChanges({})
      setAccount(undefined)
      toast.success(`Saved where ${row.name} runs in ${environment}`)
      onClose()
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
    <form
      className="flex min-h-0 flex-1 flex-col"
      onSubmit={(e) => {
        // The panel is portaled; keep its submit from reaching a page form.
        e.preventDefault()
        e.stopPropagation()
        void submit()
      }}
    >
      <div className="grid flex-1 content-start gap-4 overflow-y-auto p-4">
        <label className="flex items-center justify-between gap-3 text-[13px] font-medium">
          Runs in {environment}
          <Switch
            size="sm"
            aria-label={`Enable ${row.name} in ${environment}`}
            checked={value.enabled}
            disabled={disabled}
            onCheckedChange={(enabled) => change({ enabled })}
          />
        </label>
        {value.enabled && (
          <>
            <div className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
              <span>Runs on</span>
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
                          ? { cloudSqlNetwork: noRoute }
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
                          ? { cloudSqlNetwork: noRoute }
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
                        <SelectLabel>Accounts</SelectLabel>
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
              <span className="text-xs font-normal text-muted-foreground">
                A Cloud target, or an account directly.
              </span>
            </div>
            {owned && (
              <ConnectionChoice
                title={selected?.cloud === "kubernetes" ? "Cluster" : "Server"}
                label={`Infrastructure for ${row.name}`}
                value={value.target}
                connections={targetConnections(accounts, selected!.cloud)}
                disabled={disabled}
                automatic="Environment default"
                onChange={(target) => change({ target, endpoint: {} })}
              />
            )}
            {accountCloud && (
              <AccountChoice
                projectId={projectId}
                environment={environment}
                cloud={accountCloud}
                value={account?.cloud === accountCloud ? account.id : undefined}
                onChange={(id) => setAccount({ cloud: accountCloud, id })}
              />
            )}
            <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
              Region
              <Input
                aria-label={`Region for ${row.name}`}
                value={value.region}
                placeholder={
                  cloudChosen
                    ? "Set by the Cloud target"
                    : selected?.regions?.length
                      ? "Automatic"
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
            {row.section === "services" && (
              <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
                Tags
                <Input
                  className="font-mono"
                  aria-label={`Cloud tags for ${row.name}`}
                  value={tagsText ?? (value.cloudTags ?? []).join(", ")}
                  placeholder="prod, eu"
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
                <span className="text-xs font-normal text-muted-foreground">
                  Pick a target by tag instead of by name.
                </span>
              </label>
            )}
            {row.section === "services" && cloudTargets.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No Cloud is assigned to {environment}, so no Cloud targets are
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
            {targets.length === 0 && cloudChoices.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No available target supports this{" "}
                {row.section === "resources" ? "resource" : "workload"}. Review
                its source or kind in loomplane.yaml.
              </p>
            )}
            {(cloudRun || owned) && (
              <Collapsible
                defaultOpen={!routeValid}
                className="grid gap-3 rounded-xl ring-1 ring-border"
              >
                <CollapsibleTrigger
                  render={
                    <button
                      type="button"
                      className="group flex items-center justify-between px-3 py-2.5 text-[13px] font-medium"
                    />
                  }
                >
                  Build and network
                  <ChevronDownIcon
                    className="size-4 text-muted-foreground transition-transform group-data-[panel-open]:rotate-180"
                    aria-hidden="true"
                  />
                </CollapsibleTrigger>
                <CollapsibleContent className="grid min-w-0 gap-3 px-3 pb-3">
                  {cloudRun && (
                    <>
                      <CloudSQLApplicationFields
                        name={row.name}
                        value={route}
                        disabled={disabled}
                        onChange={(cloudSqlNetwork) =>
                          change({ cloudSqlNetwork })
                        }
                      />
                      {!routeValid && (
                        <p role="alert" className="text-xs text-destructive">
                          Choose a canonical VPC and subnet in the same host
                          project and service region, or clear both fields to
                          restore inheritance.
                        </p>
                      )}
                    </>
                  )}
                  {owned && (
                    <OwnedFields
                      row={row}
                      value={value}
                      selected={selected!}
                      accounts={accounts}
                      managedRegistry={managedRegistry}
                      disabled={disabled}
                      change={change}
                      buildChange={buildChange}
                      endpointChange={endpointChange}
                    />
                  )}
                  {connections.status === "error" && (
                    <QueryError
                      error={connections.error}
                      fallback="Infrastructure choices couldn't be loaded."
                      retry={connections.refetch}
                    />
                  )}
                </CollapsibleContent>
              </Collapsible>
            )}
          </>
        )}
        {error && (
          <CloudCallout tone="error" title="The placement wasn't saved">
            {error}
          </CloudCallout>
        )}
      </div>
      <SheetFooter className="flex-row justify-end border-t">
        {onDiscard && (
          <Button
            type="button"
            variant="ghost"
            className="mr-auto"
            onClick={() => {
              onDiscard()
              onClose()
            }}
          >
            Discard change
          </Button>
        )}
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" disabled={blocked}>
          {placement.pending && <Spinner />}
          {mode === "pending" ? "Use this placement" : "Save placement"}
        </Button>
      </SheetFooter>
    </form>
  )
}

/** OwnedFields are the build and network settings of a server or cluster. */
function OwnedFields({
  row,
  value,
  selected,
  accounts,
  managedRegistry,
  disabled,
  change,
  buildChange,
  endpointChange,
}: {
  row: PlacementRow
  value: ReturnType<typeof placementSettings>
  selected: Provider
  accounts: Connection[]
  managedRegistry?: string
  disabled: boolean
  change: (patch: PlacementFields) => void
  buildChange: (patch: NonNullable<PlacementFields["build"]>) => void
  endpointChange: (patch: NonNullable<PlacementFields["endpoint"]>) => void
}) {
  const field = "grid min-w-0 gap-1.5 text-[12.5px] font-medium"
  return (
    <>
      <ConnectionChoice
        title="Builder"
        label={`Builder for ${row.name}`}
        value={value.build?.builder}
        connections={targetConnections(accounts, "server")}
        disabled={disabled || !!value.build?.image}
        automatic={
          selected.cloud === "server"
            ? "Use target server"
            : managedRegistry
              ? "Build in cluster"
              : "Cluster default or SSH builder"
        }
        onChange={(builder) => buildChange({ builder })}
      />
      <ConnectionChoice
        title="Registry"
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
          managedRegistry && !value.build?.builder && !value.build?.image
            ? "Managed cluster registry"
            : "No registry selected"
        }
        onChange={(registry) => buildChange({ registry })}
      />
      {managedRegistry &&
        !value.build?.builder &&
        !value.build?.registry &&
        !value.build?.image && (
          <p className="text-xs text-muted-foreground">
            Builds run in this cluster and publish to {managedRegistry}.
            Loomplane manages the registry and image pull secrets.
          </p>
        )}
      <label className={field}>
        Build context
        <Input
          aria-label={`Build context for ${row.name}`}
          value={value.build?.context ?? ""}
          placeholder="Repository root"
          disabled={disabled}
          onChange={(e) => buildChange({ context: e.target.value })}
        />
      </label>
      <label className={field}>
        Image
        <Input
          className="font-mono"
          aria-label={`Image for ${row.name}`}
          value={value.build?.image ?? ""}
          placeholder="Built from source"
          disabled={disabled}
          onChange={(e) => buildChange({ image: e.target.value })}
        />
      </label>
      {row.kind !== "worker" && (
        <>
          <label className={field}>
            Health check
            <Input
              aria-label={`Readiness path for ${row.name}`}
              value={value.healthPath ?? ""}
              placeholder="/ready"
              disabled={disabled}
              onChange={(e) => change({ healthPath: e.target.value })}
            />
          </label>
          {selected.cloud === "server" ? (
            <label className={field}>
              Published host port
              <Input
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
              <label className={field}>
                Hostname
                <Input
                  aria-label={`Hostname for ${row.name}`}
                  value={value.endpoint?.hostname ?? ""}
                  placeholder="api.example.com"
                  disabled={disabled}
                  onChange={(e) => endpointChange({ hostname: e.target.value })}
                />
              </label>
              <label className={field}>
                Ingress class
                <Input
                  aria-label={`Ingress class for ${row.name}`}
                  value={value.endpoint?.ingressClass ?? ""}
                  placeholder="Cluster default"
                  disabled={disabled}
                  onChange={(e) =>
                    endpointChange({ ingressClass: e.target.value })
                  }
                />
              </label>
              <label className={field}>
                TLS secret
                <Input
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
      {selected.logNote && (
        <p className="text-xs text-muted-foreground">{selected.logNote}</p>
      )}
    </>
  )
}

function ConnectionChoice({
  title,
  label,
  value,
  connections,
  disabled,
  automatic,
  onChange,
}: {
  title: string
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
      <span>{title}</span>
      <Select
        value={value || "automatic"}
        onValueChange={(v) => v && onChange(v === "automatic" ? "" : v)}
      >
        <SelectTrigger
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

/**
 * AccountChoice picks the account an environment deploys to a cloud with.
 * It's shared by every service on that cloud in the environment, and the
 * planner reads it.
 */
function AccountChoice({
  projectId,
  environment,
  cloud,
  value,
  onChange,
}: {
  projectId: string
  environment: string
  cloud: string
  value?: string
  onChange: (id: string) => void
}) {
  const list = useQuery(useConnectionsList)
  const chosen = useQuery(useEnvironmentConnectionsGet, {
    path: { id: projectId, environment },
  })
  const { can } = useOrganization()
  const name = cloudLabel(cloud)
  if (list.status === "error" || chosen.status === "error")
    return (
      <QueryError
        error={list.error ?? chosen.error}
        fallback="Cloud accounts couldn't be loaded."
        retry={() => Promise.allSettled([list.refetch(), chosen.refetch()])}
      />
    )
  if (!list.data || !chosen.data) return <Skeleton className="h-14" />
  const ready = list.data.connections.filter(
    (c) => c.cloud === cloud && c.status === "ready"
  )
  const id =
    value ??
    String(
      chosen.data.connections[cloud] ?? (ready.length === 1 ? ready[0].id : "")
    )
  const selected = ready.find((c) => c.id === id)
  return (
    <div className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
      <span>{name} account</span>
      {ready.length === 0 ? (
        <Link
          to="/integrations"
          className="text-[13px] font-normal underline underline-offset-4"
        >
          Connect {name}
        </Link>
      ) : (
        <Select
          value={selected?.id ?? null}
          onValueChange={(v) => v && onChange(v)}
        >
          <SelectTrigger
            className="w-full min-w-0"
            aria-label={`Account for ${name} in ${environment}`}
            disabled={!can("cloud_account:manage")}
          >
            <SelectValue
              placeholder={
                id ? "The chosen account isn't ready" : "Choose an account"
              }
            >
              {selected ? `${selected.name} · ${selected.account}` : undefined}
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
      <span className="text-xs font-normal text-muted-foreground">
        Every service on {name} in {environment} deploys with this account.
      </span>
    </div>
  )
}
