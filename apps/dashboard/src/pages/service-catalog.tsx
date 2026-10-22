import { useCallback, useRef, useState } from "react"
import { CheckIcon, ChevronLeftIcon, DatabaseIcon, ZapIcon } from "lucide-react"
import { toast } from "sonner"

import {
  useConnectionsList,
  useProvidersList,
  useEnvironmentsList,
  useBindingsList,
  useServiceBackupsList,
  useServiceBackupsCreate,
  useServiceBackupsRetry,
  useServiceRestoresList,
  useServiceRestoresCreate,
  useServiceRestoresDelete,
  useBackupPoliciesList,
  useBackupPoliciesSet,
} from "@/client/forge/loomplane/hooks"
import type { Restore } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { Page } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { QueryError } from "@/components/query-error"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
  ZeroStateMedia,
} from "@/components/zero-state"
import { Button, buttonVariants } from "@/components/ui/button"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ProductIcon } from "@/components/product-icon"
import { cn } from "@/lib/utils"
import { catalog } from "@/pages/services"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@/components/ui/alert-dialog"
import { useOrganization } from "@/lib/organization"
import { databaseTargetProvider } from "@/lib/catalog-targets"
import { deployDatabase } from "@/lib/catalog"
import { databaseRecipes, type DatabaseKind } from "@/lib/catalog-recipes"
import { Link } from "@/lib/router"
import { formatDate } from "@/lib/format"
import { recoverySelection } from "@/lib/service-recovery"
import { BackupRetentionReview } from "@/components/backup-retirement/review"
import { RecoveryPointRow } from "@/components/recovery-point-row"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { BackupSchedule } from "@/components/backup-schedule"
import { useDeploymentWrites } from "@/lib/deployment-writes"
import {
  isManagedDatabaseProvider,
  managedDatabaseProfile,
} from "@/lib/managed-catalog"
import { NativeRecovery } from "@/components/native-recovery"
import { ManagedDatabaseSettings } from "@/components/managed-database-settings"

/**
 * ServiceCatalog deploys one engine and recovers what's deployed: the page a
 * catalog card opens. kind picks the engine it starts on.
 */
export function ServiceCatalog({
  kind = "postgres",
}: { kind?: DatabaseKind } = {}) {
  const recipe = databaseRecipes[kind]
  const entry = catalog.find((e) => e.kind === kind)
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Services", to: "/services" },
          { label: recipe.title },
        ]}
      />
      <Page>
        <Link
          to="/services"
          className="-mb-2 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          Service catalog
        </Link>
        <header className="flex flex-wrap items-center gap-4">
          <ProductIcon
            product={entry?.product}
            fallback={ZapIcon}
            label={recipe.title}
            size="xl"
          />
          <div className="grid min-w-0 flex-1 gap-1">
            <h1 className="flex flex-wrap items-baseline gap-2 text-[22px] leading-tight font-semibold tracking-[-0.025em] md:text-2xl">
              {recipe.title}
              <span className="font-mono text-sm font-normal text-muted-foreground">
                {recipe.version}
              </span>
            </h1>
            <p className="max-w-[72ch] text-[13.5px] leading-relaxed text-muted-foreground">
              {entry?.blurb ?? recipe.description}
            </p>
          </div>
        </header>
        <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
          <DatabaseDeployment key={kind} kind={kind} />
          <WhatYouGet kind={kind} />
        </div>
      </Page>
    </>
  )
}

/** WhatYouGet is the engine's profile, and where its backups live after. */
function WhatYouGet({ kind }: { kind: DatabaseKind }) {
  const recipe = databaseRecipes[kind]
  const entry = catalog.find((e) => e.kind === kind)
  return (
    <aside
      aria-label="What you get"
      className="grid gap-3 rounded-[14px] bg-subtle p-4 ring-1 ring-border"
    >
      <h2 className="text-[13px] font-semibold">What you get</h2>
      <p className="text-[12.5px] leading-relaxed text-muted-foreground">
        {recipe.description}
      </p>
      {entry && (
        <ul className="grid gap-1.5 text-[12.5px]">
          {entry.features.map((f) => (
            <li key={f} className="flex items-center gap-2">
              <CheckIcon className="size-3.5 text-overdye" />
              {f}
            </li>
          ))}
          <li className="flex items-center gap-2 text-muted-foreground">
            <span className="size-3.5" />
            Runs on {entry.runsOn}
          </li>
        </ul>
      )}
      <p className="border-t pt-3 text-[12px] leading-relaxed text-muted-foreground">
        Point-in-time recovery isn&apos;t available yet. Once it&apos;s
        deployed, backups, test restores and recovery copies are on the
        service&apos;s project page, under Backups.
      </p>
    </aside>
  )
}

// What each connected account would run the database as.
function targetMode(
  provider: string | undefined,
  details: Record<string, string>
): string {
  if (provider === "rds") return `Managed · Amazon RDS · ${details.region}`
  if (provider === "cloud-sql")
    return `Managed · Cloud SQL · ${details.project_id}`
  if (provider === "kubernetes")
    return `Single instance · Kubernetes · ${details.namespace}`
  return "Single instance · Docker"
}

// The accounts that can run each engine, for the empty state.
const runsOnClouds: Record<DatabaseKind, string[]> = {
  postgres: ["aws", "gcp", "server", "kubernetes"],
  mysql: ["server"],
  mariadb: ["server"],
  redis: ["server"],
  valkey: ["server"],
}

function DatabaseDeployment({
  kind,
  onCreated,
}: {
  kind: DatabaseKind
  onCreated?: (id: string) => void
}) {
  const { can } = useOrganization()
  const connections = useQuery(useConnectionsList)
  const providers = useQuery(useProvidersList)
  const offers =
    providers.status === "success" ? (providers.data?.providers ?? []) : []
  const targets =
    connections.data?.connections.filter((connection) => {
      const provider = databaseTargetProvider(connection, kind)
      return (
        !!provider &&
        (!isManagedDatabaseProvider(provider) ||
          !!managedDatabaseProfile(offers, provider))
      )
    }) ?? []
  const recipe = databaseRecipes[kind]
  const [name, setName] = useState<string>(recipe.title)
  const [target, setTarget] = useState("")
  const [regionChoice, setRegionChoice] = useState({ target: "", value: "" })
  const [daily, setDaily] = useState(true)
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState("")
  const [error, setError] = useState("")
  const [created, setCreated] = useState("")
  const defaultTarget = targets[0]?.id ?? ""
  if (
    !target &&
    defaultTarget &&
    connections.status === "success" &&
    providers.status !== "pending"
  )
    setTarget(defaultTarget)
  const locked = pending || !!created
  const allowed = [
    "project:create",
    "project:update",
    "deployment:create",
    "deployment:approve",
  ].every(can)
  const selected = target
    ? targets.some((connection) => connection.id === target)
      ? target
      : ""
    : (targets[0]?.id ?? "")
  const selectedTarget = targets.find(
    (connection) => connection.id === selected
  )
  const targetProvider = selectedTarget
    ? databaseTargetProvider(selectedTarget, kind)
    : undefined
  const managed = isManagedDatabaseProvider(targetProvider)
  const profile = targetProvider
    ? managedDatabaseProfile(offers, targetProvider)
    : undefined
  const selectedProvider = offers.find((offer) => offer.name === targetProvider)
  const regions = managed
    ? targetProvider === "rds"
      ? [selectedTarget!.details.region]
      : (selectedProvider?.regions ?? [])
    : []
  const region =
    regionChoice.target === selected && regions.includes(regionChoice.value)
      ? regionChoice.value
      : (regions[0] ?? "")
  async function deploy() {
    if (locked || !targetProvider) return
    setPending(true)
    setError("")
    try {
      await deployDatabase(
        {
          kind,
          name: name.trim(),
          target: selected,
          provider: targetProvider,
          targetSettings: selectedTarget?.details,
          daily: !managed && daily && can("service_backup:manage"),
          managed:
            managed && profile
              ? {
                  profile,
                  accountIdentity: selectedTarget!.accountIdentity!,
                  region,
                }
              : undefined,
        },
        setProgress,
        (id) => {
          setCreated(id)
          onCreated?.(id)
        }
      )
      toast.success(`${recipe.title} deployed`)
    } catch (cause) {
      setError(messageOf(cause, "Deployment could not finish"))
    } finally {
      setPending(false)
    }
  }
  return (
    <section
      aria-label="Deploy database"
      className="grid gap-5 rounded-[14px] bg-card p-5 ring-1 ring-border"
    >
      {providers.status === "error" && connections.status !== "error" && (
        <QueryError
          error={providers.error}
          fallback="Could not load cloud profiles"
          retry={providers.refetch}
        />
      )}
      {connections.status === "error" ? (
        <QueryError
          error={connections.error}
          fallback="Could not load targets"
          retry={connections.refetch}
        />
      ) : connections.status === "pending" || providers.status === "pending" ? (
        <p role="status" className="text-[13px] text-muted-foreground">
          Loading checked targets…
        </p>
      ) : providers.status === "error" &&
        !targets.length ? null : !targets.length ? (
        <CatalogEmpty
          title={
            kind === "postgres"
              ? "Connect a database target"
              : "Connect a Docker server"
          }
          description={
            kind === "postgres"
              ? "Check an AWS or Google Cloud account, a Docker server, or a Kubernetes namespace with retained storage and recovery permissions."
              : "Add and check an SSH server connection before deploying a database."
          }
        >
          <span className="flex basis-full justify-center gap-1.5">
            {runsOnClouds[kind].map((c) => (
              <CloudGlyph key={c} cloud={c} size="sm" />
            ))}
          </span>
          <Button
            variant="outline"
            nativeButton={false}
            render={<Link to="/integrations" />}
          >
            Open integrations
          </Button>
        </CatalogEmpty>
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void deploy()
          }}
          className="grid gap-5"
        >
          <fieldset className="grid gap-2">
            <legend className="mb-2 text-[13px] font-semibold">
              Where it runs
            </legend>
            <div
              role="radiogroup"
              aria-label="Deployment target"
              className="grid gap-2 sm:grid-cols-2"
            >
              {targets.map((c) => {
                const on = c.id === selected
                return (
                  <button
                    key={c.id}
                    type="button"
                    role="radio"
                    aria-checked={on}
                    disabled={locked || !allowed}
                    onClick={() => setTarget(c.id)}
                    className={cn(
                      "flex min-w-0 items-center gap-3 rounded-xl bg-card px-3 py-2.5 text-left ring-1 transition-shadow disabled:opacity-60",
                      on
                        ? "ring-2 ring-primary"
                        : "ring-border hover:ring-foreground/25"
                    )}
                  >
                    <CloudGlyph cloud={c.cloud} size="md" />
                    <span className="grid min-w-0 gap-0.5">
                      <span className="truncate text-[13px] font-medium">
                        {c.name}
                      </span>
                      <span className="truncate text-xs text-muted-foreground">
                        {targetMode(databaseTargetProvider(c, kind), c.details)}
                      </span>
                    </span>
                  </button>
                )
              })}
            </div>
            <Link
              to="/integrations"
              className="justify-self-start text-[12.5px] font-medium text-brand-ink hover:underline"
            >
              Connect another account
            </Link>
          </fieldset>
          {managed && profile ? (
            <ManagedDatabaseSettings profile={profile} />
          ) : targetProvider === "kubernetes" ? (
            <p className="text-[12.5px] leading-relaxed text-muted-foreground">
              Private endpoint, restricted application role and a 10 GiB
              retained PVC on {selectedTarget?.details.storage_class}. Restore
              copies require an enforcing NetworkPolicy CNI. Keep backups
              outside this cluster.
            </p>
          ) : null}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid min-w-0 gap-1.5">
              <Label htmlFor="database-name">Project name</Label>
              <Input
                id="database-name"
                value={name}
                maxLength={100}
                required
                disabled={locked || !allowed}
                onChange={(e) => setName(e.target.value)}
              />
              <span className="text-xs text-muted-foreground">
                {recipe.title} gets its own project, so its backups and
                variables stay with it.
              </span>
            </div>
            {managed && (
              <div className="grid min-w-0 content-start gap-1.5">
                <Label htmlFor="database-region">Region</Label>
                {targetProvider === "rds" ? (
                  <Input id="database-region" readOnly value={region} />
                ) : (
                  <Select
                    value={region}
                    onValueChange={(value) =>
                      setRegionChoice({ target: selected, value: value ?? "" })
                    }
                    disabled={locked || !allowed}
                  >
                    <SelectTrigger id="database-region" className="w-full">
                      <SelectValue>{region || "Choose a region"}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {regions.map((value) => (
                        <SelectItem key={value} value={value}>
                          {value}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
            )}
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
            {!managed && can("service_backup:manage") ? (
              <label className="flex items-center gap-2 text-[13px]">
                <Checkbox
                  checked={daily}
                  onCheckedChange={(value) => setDaily(!!value)}
                  disabled={locked}
                />
                Daily backups
              </label>
            ) : managed && selectedTarget ? (
              <span className="text-xs break-words text-muted-foreground">
                Cloud account: {selectedTarget.account}
              </span>
            ) : (
              <span />
            )}
            <Button
              type="submit"
              disabled={
                locked ||
                !allowed ||
                !name.trim() ||
                !selected ||
                (managed && !region)
              }
            >
              {pending ? "Deploying…" : `Deploy ${recipe.title}`}
            </Button>
          </div>
        </form>
      )}
      {!allowed && (
        <p className="text-[13px] text-muted-foreground">
          Deployment requires project creation, project update and deployment
          approval access.
        </p>
      )}
      {progress && !error && (
        <p role="status" className="text-[13px]">
          {progress}
        </p>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {created && (
        <div className="flex flex-wrap items-center gap-3 rounded-xl bg-overdye-tint px-4 py-3">
          <span className="flex-1 text-[13px]">
            {recipe.title} is on its own project now, with its backups under
            Backups.
          </span>
          <Link
            to="/projects/$id"
            params={{ id: created }}
            className={buttonVariants({ size: "sm" })}
          >
            Open service project
          </Link>
          <Button
            size="sm"
            variant="ghost"
            disabled={pending}
            onClick={() => {
              setCreated("")
              setError("")
              setProgress("")
              setName("")
            }}
          >
            New service
          </Button>
        </div>
      )}
    </section>
  )
}

/**
 * ServiceRecovery is a project's database recovery. It opens on production
 * and the first database there, unless environment (an environment ID) and
 * resource choose where it starts; both can still be changed.
 */
export function ServiceRecovery({
  project,
  environment,
  resource,
}: {
  project: string
  environment?: string
  resource?: string
}) {
  if (!environment)
    return (
      <Recovery
        project={project}
        initialEnvironment="production"
        initialResource={resource ?? ""}
      />
    )
  return (
    <PreselectedRecovery
      project={project}
      environment={environment}
      resource={resource ?? ""}
    />
  )
}

// The recovery controls key environments by name, so a preselected ID waits
// for the environments to say which name it is.
function PreselectedRecovery({
  project,
  environment,
  resource,
}: {
  project: string
  environment: string
  resource: string
}) {
  const environments = useQuery(useEnvironmentsList, { path: { id: project } })
  if (environments.status === "error")
    return (
      <QueryError
        error={environments.error}
        fallback="Could not load recovery state"
        retry={environments.refetch}
      />
    )
  if (!environments.data) return <p role="status">Loading recovery state…</p>
  const name =
    environments.data.environments.find((e) => e.id === environment)?.name ??
    "production"
  return (
    <Recovery
      key={`${environment}:${resource}`}
      project={project}
      initialEnvironment={name}
      initialResource={resource}
    />
  )
}

function Recovery({
  project,
  initialEnvironment,
  initialResource,
}: {
  project: string
  initialEnvironment: string
  initialResource: string
}) {
  const [showNative, setShowNative] = useState(false)
  const { can } = useOrganization()
  const environments = useQuery(useEnvironmentsList, { path: { id: project } })
  const [environment, setEnvironment] = useState(initialEnvironment)
  const bindings = useQuery(useBindingsList, {
    path: { id: project },
    query: { environment },
  })
  const backups = useQuery(useServiceBackupsList, { path: { id: project } })
  const restores = useQuery(useServiceRestoresList, { path: { id: project } })
  const policies = useQuery(useBackupPoliciesList, { path: { id: project } })
  const createBackup = useMutation(useServiceBackupsCreate)
  const retryBackup = useMutation(useServiceBackupsRetry)
  const createRestore = useMutation(useServiceRestoresCreate)
  const deleteRestore = useMutation(useServiceRestoresDelete)
  const policySet = useMutation(useBackupPoliciesSet)
  const [deleting, setDeleting] = useState<Restore | null>(null)
  const { busy, onPendingChange } = useDeploymentWrites(project)
  const requests = useRef<Record<string, string>>({})
  const [resource, setResource] = useState(initialResource)
  const environmentID = environments.data?.environments.find(
    (e) => e.name === environment
  )?.id
  const { resources, selected, live, points, copies } = recoverySelection(
    bindings.data?.bindings ?? [],
    backups.data?.backups ?? [],
    restores.data?.restores ?? [],
    environmentID,
    resource
  )
  const managedRecovery =
    !selected &&
    bindings.data?.bindings.some(
      (binding) =>
        binding.subject === "resource" &&
        ["rds", "cloud-sql"].includes(binding.provider) &&
        binding.environmentId === environmentID
    )
  const policy = policies.data?.policies.find(
    (p) => p.environment === environment && p.resource === selected
  )
  const active =
    points.some((b) => ["queued", "running", "retiring"].includes(b.status)) ||
    copies.some((r) => ["queued", "running", "deleting"].includes(r.status))
  const { refetch: refreshBackups } = backups
  const { refetch: refreshRestores } = restores
  const { refetch: refreshBindings } = bindings
  const { refetch: refreshPolicies } = policies
  const pollRecovery = useCallback(
    () =>
      Promise.allSettled([
        refreshBackups(),
        refreshRestores(),
        refreshBindings(),
        refreshPolicies(),
      ]),
    [refreshBackups, refreshRestores, refreshBindings, refreshPolicies]
  )
  useRecoveryPolling(active, pollRecovery)
  async function action(
    key: string,
    run: (requestId: string) => Promise<unknown>
  ) {
    if (busy) return
    onPendingChange(true)
    const requestId = (requests.current[key] ??= crypto.randomUUID())
    try {
      await run(requestId)
      delete requests.current[key]
      await Promise.all([
        backups.refetch(),
        restores.refetch(),
        policies.refetch(),
      ])
      toast.success("Request accepted")
    } catch (error) {
      toast.error(
        messageOf(error, "The operation could not finish. You can retry.")
      )
    } finally {
      onPendingChange(false)
    }
  }
  const backupNow = () =>
    void action(`backup:${environment}:${selected}`, (requestId) =>
      createBackup.mutateAsync({
        path: { id: project },
        body: { environment, resource: selected, requestId },
      })
    )
  const failed = [environments, bindings, backups, restores, policies].find(
    (query) => query.status === "error"
  )
  if (failed)
    return (
      <QueryError
        error={failed.error}
        fallback="Could not load recovery state"
        retry={failed.refetch}
      />
    )
  if (
    [environments, bindings, backups, restores, policies].some(
      (query) => query.status === "pending"
    )
  )
    return <p role="status">Loading recovery state…</p>
  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={environment}
          disabled={busy}
          onValueChange={(value) => setEnvironment(value ?? "production")}
        >
          <SelectTrigger aria-label="Recovery environment" className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {environments.data?.environments.map((e) => (
              <SelectItem key={e.id} value={e.name}>
                {e.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          size="sm"
          variant="outline"
          aria-expanded={showNative}
          onClick={() => setShowNative((current) => !current)}
        >
          Cloud snapshots
        </Button>
        {resources.length > 0 && (
          <Select
            value={selected}
            disabled={busy}
            onValueChange={(value) => setResource(value ?? "")}
          >
            <SelectTrigger aria-label="Database" className="w-44">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {resources.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {selected && live && can("service_backup:manage") && (
          <>
            <Button size="sm" disabled={busy} onClick={backupNow}>
              Back up now
            </Button>
            <BackupSchedule
              key={`${environment}:${selected}:${policy?.everyHours}:${policy?.enabled}`}
              policy={policy}
              disabled={busy}
              save={(everyHours, enabled) =>
                void action("policy", () =>
                  policySet.mutateAsync({
                    path: { id: project },
                    body: {
                      environment,
                      resource: selected,
                      everyHours,
                      enabled,
                    },
                  })
                )
              }
            />
          </>
        )}
        {selected && points.length > 0 && (
          <BackupRetentionReview
            key={`${environment}:${selected}`}
            project={project}
            points={points}
            onChanged={refreshBackups}
          />
        )}
      </div>
      {showNative && (
        <NativeRecovery
          key={`${project}:${environmentID}`}
          project={project}
          environment={environmentID}
        />
      )}
      {managedRecovery ? (
        <CatalogEmpty
          title="Managed database recovery"
          description="Open Cloud snapshots to inspect captured RDS history. Restore and PITR controls remain in your cloud account."
        >
          <Button
            variant="outline"
            nativeButton={false}
            render={<Link to="/projects/$id" params={{ id: project }} />}
          >
            Open project
          </Button>
        </CatalogEmpty>
      ) : !selected ? (
        <CatalogEmpty
          title="No catalog database in this environment"
          description="Deploy a database above, or choose the environment where it runs."
        />
      ) : (
        <>
          {!live && (
            <p className="text-sm text-muted-foreground">
              The source service has been removed. Recovery points and test
              copies remain available.
            </p>
          )}
          <div className="text-sm text-muted-foreground">
            {policy?.enabled
              ? `Backups every ${policy.everyHours} hours. Next: ${formatDate(policy.nextAt)}.`
              : "Backups run when requested."}{" "}
            Manage deletion separately in Review retention.{" "}
            {policy?.lastError && (
              <span className="text-destructive">{policy.lastError}</span>
            )}
          </div>
          {!points.length ? (
            <CatalogEmpty
              title="No recovery points yet"
              description="Create a native backup before testing recovery. A local volume alone cannot recover from host loss."
            >
              {live && can("service_backup:manage") && (
                <Button size="sm" disabled={busy} onClick={backupNow}>
                  Create first backup
                </Button>
              )}
            </CatalogEmpty>
          ) : (
            <div className="min-w-0 overflow-x-auto rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Backup</TableHead>
                    <TableHead className="hidden sm:table-cell">
                      Status
                    </TableHead>
                    <TableHead className="hidden sm:table-cell">
                      Restore evidence
                    </TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {points.map((b) => (
                    <RecoveryPointRow
                      key={b.id}
                      project={project}
                      backup={b}
                      busy={busy}
                      onChanged={refreshBackups}
                      onRestore={(id) =>
                        void action(`restore:${id}`, (requestId) =>
                          createRestore.mutateAsync({
                            path: { id },
                            body: { requestId },
                          })
                        )
                      }
                      onRetry={(id) =>
                        void action(`retry:${id}`, () =>
                          retryBackup.mutateAsync({ path: { id } })
                        )
                      }
                    />
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </>
      )}
      {!managedRecovery && (
        <div className="min-w-0">
          <h3 className="mb-2 text-sm font-semibold">
            Isolated recovery copies
          </h3>
          <p className="mb-3 text-sm text-muted-foreground">
            Copies use a private network, never replace a live database, and are
            scheduled for removal after 24 hours. Database checks do not test
            your application.
          </p>
          {!copies.length ? (
            <CatalogEmpty
              title="No recovery copies"
              description="Use Test restore on a completed backup to verify it in a temporary database."
            />
          ) : (
            <div className="min-w-0 overflow-x-auto rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Copy</TableHead>
                    <TableHead className="hidden sm:table-cell">
                      Status
                    </TableHead>
                    <TableHead className="hidden sm:table-cell">
                      Cleanup deadline
                    </TableHead>
                    <TableHead className="text-right">Action</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {copies.map((r) => (
                    <TableRow key={r.id}>
                      <TableCell className="whitespace-normal">
                        <div>{formatDate(r.createdAt)}</div>
                        <div className="mt-1 grid gap-1 text-xs sm:hidden">
                          <Badge variant="secondary">{r.status}</Badge>
                          <span>
                            {r.status === "deleted"
                              ? "Removed"
                              : `Cleanup: ${formatDate(r.expiresAt)}`}
                          </span>
                          {r.error && (
                            <span className="text-destructive">{r.error}</span>
                          )}
                        </div>
                        {r.verifiedAt && (
                          <div className="text-xs text-muted-foreground">
                            Restore verified in{" "}
                            {(r.durationMs / 1000).toFixed(1)}s
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <Badge variant="secondary">{r.status}</Badge>
                        {r.error && (
                          <p className="max-w-xs text-xs text-destructive">
                            {r.error}
                          </p>
                        )}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        {r.status === "deleted"
                          ? "Removed"
                          : formatDate(r.expiresAt)}
                      </TableCell>
                      <TableCell className="text-right">
                        {["succeeded", "failed", "cleanup_failed"].includes(
                          r.status
                        ) &&
                          can("service_restore:manage") && (
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={busy}
                              onClick={() => setDeleting(r)}
                            >
                              Remove copy
                            </Button>
                          )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </div>
      )}
      <AlertDialog
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove this recovery copy?</AlertDialogTitle>
            <AlertDialogDescription>
              The temporary database and its volume will be deleted. Its backup,
              verification record and live source remain available.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (deleting)
                  void action(`delete:${deleting.id}`, () =>
                    deleteRestore.mutateAsync({ path: { id: deleting.id } })
                  )
                setDeleting(null)
              }}
            >
              Remove copy
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
function CatalogEmpty({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children?: React.ReactNode
}) {
  return (
    <ZeroState>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <DatabaseIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      {children && <ZeroStateContent>{children}</ZeroStateContent>}
    </ZeroState>
  )
}
