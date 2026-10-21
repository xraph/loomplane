import { useCallback, useRef, useState } from "react"
import { DatabaseIcon } from "lucide-react"
import { toast } from "sonner"

import {
  useConnectionsList,
  useProvidersList,
  useProjectsList,
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
import { Page, PageHeading } from "@/components/page"
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
import { Button } from "@/components/ui/button"
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
import {
  databaseRecipes,
  isDatabaseKind,
  type DatabaseKind,
} from "@/lib/catalog-recipes"
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

export function ServiceCatalog() {
  const { can } = useOrganization()
  const projects = useQuery(useProjectsList)
  const [project, setProject] = useState("")
  const selected = project || projects.data?.projects[0]?.id || ""
  const { busy: projectBusy } = useDeploymentWrites(selected)
  return (
    <>
      <SiteHeader crumbs={[{ label: "Services" }]} />
      <Page className="gap-5">
        <PageHeading
          title="Services"
          description="Deploy databases with persistent storage and native backups."
        />
        <DatabaseDeployment
          onCreated={(id) => {
            setProject(id)
            void projects.refetch()
          }}
        />
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-base font-semibold">Service recovery</h2>
          <Select
            value={selected}
            onValueChange={(value) => setProject(value ?? "")}
          >
            <SelectTrigger
              aria-label="Recovery project"
              disabled={projectBusy}
              className="w-full sm:w-64"
            >
              <SelectValue placeholder="Choose a project">
                {projects.data?.projects.find((p) => p.id === selected)?.name ??
                  "Choose a project"}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {projects.data?.projects.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {projects.status === "error" ? (
          <QueryError
            error={projects.error}
            fallback="Could not load projects"
            retry={projects.refetch}
          />
        ) : projects.status === "pending" ? (
          <p role="status">Loading projects…</p>
        ) : !selected ? (
          <CatalogEmpty
            title="No service projects yet"
            description="Deploy a database above to create a project with persistent storage."
          />
        ) : !can("service_backup:read") ? (
          <Alert>
            <AlertDescription>
              You need service backup read access to view recovery points.
            </AlertDescription>
          </Alert>
        ) : (
          <ServiceRecovery key={selected} project={selected} />
        )}
      </Page>
    </>
  )
}
function DatabaseDeployment({
  onCreated,
}: {
  onCreated: (id: string) => void
}) {
  const { can } = useOrganization()
  const connections = useQuery(useConnectionsList)
  const providers = useQuery(useProvidersList)
  const offers =
    providers.status === "success" ? (providers.data?.providers ?? []) : []
  const [kind, setKind] = useState<DatabaseKind>("postgres")
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
  const [name, setName] = useState<string>("PostgreSQL")
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
          onCreated(id)
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
    <section className="rounded-xl border p-4" aria-label="Deploy database">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <DatabaseIcon className="size-5" />
        <h2 className="font-semibold">Database</h2>
        <div className="min-w-40">
          <Label className="sr-only" htmlFor="database-engine">
            Database engine
          </Label>
          <Select
            value={kind}
            disabled={locked}
            onValueChange={(value) => {
              if (!isDatabaseKind(value)) return
              if (name === recipe.title) setName(databaseRecipes[value].title)
              setKind(value)
              setTarget("")
            }}
          >
            <SelectTrigger id="database-engine" className="w-full">
              <SelectValue>
                {recipe.title} {recipe.version}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {Object.entries(databaseRecipes).map(([value, entry]) => (
                <SelectItem key={value} value={value}>
                  {entry.title} {entry.version}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <Badge variant="secondary">
          {managed
            ? `Managed · ${selectedProvider?.title}`
            : targetProvider
              ? `Single instance · ${targetProvider === "kubernetes" ? "Kubernetes" : "Docker"}`
              : "Choose a target"}
        </Badge>
      </div>
      {managed && profile ? (
        <ManagedDatabaseSettings profile={profile} />
      ) : targetProvider ? (
        <p className="mb-4 text-sm text-muted-foreground">
          {targetProvider === "kubernetes"
            ? `Private endpoint, restricted application role and a 10 GiB retained PVC on ${selectedTarget?.details.storage_class}. Restore copies require an enforcing NetworkPolicy CNI. Keep backups outside this cluster.`
            : recipe.description}{" "}
          Point-in-time recovery is not available in this profile.
        </p>
      ) : null}
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
        <p role="status">Loading checked targets…</p>
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
          className="flex flex-wrap items-end gap-3"
        >
          <div className="grid min-w-0 basis-full gap-1.5 sm:min-w-40 sm:flex-1 sm:basis-0">
            <Label htmlFor="database-name">Project name</Label>
            <Input
              id="database-name"
              value={name}
              maxLength={100}
              required
              disabled={locked || !allowed}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="grid min-w-0 basis-full gap-1.5 sm:min-w-48 sm:flex-1 sm:basis-0">
            <Label htmlFor="database-target">Deployment target</Label>
            <Select
              value={selected}
              disabled={locked || !allowed}
              onValueChange={(value) => setTarget(value ?? "")}
            >
              <SelectTrigger id="database-target" className="w-full">
                <SelectValue>
                  {targets.find((c) => c.id === selected)?.name ??
                    "Choose a target"}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {targets.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {managed && (
            <div className="grid min-w-0 basis-full gap-1.5 sm:min-w-40 sm:flex-1 sm:basis-0">
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
          {!managed && can("service_backup:manage") && (
            <label className="flex h-9 items-center gap-2 text-sm">
              <Checkbox
                checked={daily}
                onCheckedChange={(value) => setDaily(!!value)}
                disabled={locked}
              />
              Daily backups
            </label>
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
        </form>
      )}
      {managed && selectedTarget && (
        <p className="mt-2 text-xs break-words text-muted-foreground">
          Cloud account: {selectedTarget.account}
        </p>
      )}
      {!allowed && (
        <p className="mt-2 text-sm text-muted-foreground">
          Deployment requires project creation, project update and deployment
          approval access.
        </p>
      )}
      {progress && !error && (
        <p role="status" className="mt-3 text-sm">
          {progress}
        </p>
      )}
      {error && (
        <Alert variant="destructive" className="mt-3">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {created && (
        <div className="mt-2 flex flex-wrap items-center gap-3">
          <Link
            to="/projects/$id"
            params={{ id: created }}
            className="inline-block text-sm underline"
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
function ServiceRecovery({ project }: { project: string }) {
  const [showNative, setShowNative] = useState(false)
  const { can } = useOrganization()
  const environments = useQuery(useEnvironmentsList, { path: { id: project } })
  const [environment, setEnvironment] = useState("production")
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
  const [resource, setResource] = useState("")
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
    <ZeroState className="border">
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
