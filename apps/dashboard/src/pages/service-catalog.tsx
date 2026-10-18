import { useEffect, useRef, useState } from "react"
import { DatabaseIcon, RotateCcwIcon } from "lucide-react"
import { toast } from "sonner"

import {
  useConnectionsList,
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
import { deployPostgres } from "@/lib/catalog"
import { Link } from "@/lib/router"
import { formatDate } from "@/lib/format"
import { recoverySelection } from "@/lib/service-recovery"
import { BackupSchedule } from "@/components/backup-schedule"
import { useDeploymentWrites } from "@/lib/deployment-writes"

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
          description="Deploy PostgreSQL with persistent storage and native backups."
        />
        <PostgresDeployment
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
            description="Deploy PostgreSQL above to create a project with persistent storage."
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
function PostgresDeployment({
  onCreated,
}: {
  onCreated: (id: string) => void
}) {
  const { can } = useOrganization()
  const connections = useQuery(useConnectionsList)
  const targets =
    connections.data?.connections.filter(
      (c) =>
        c.cloud === "server" &&
        c.status === "ready" &&
        ["amd64", "arm64"].includes(c.details.architecture)
    ) ?? []
  const [name, setName] = useState("PostgreSQL")
  const [target, setTarget] = useState("")
  const [daily, setDaily] = useState(true)
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState("")
  const [error, setError] = useState("")
  const [created, setCreated] = useState("")
  const allowed = [
    "project:create",
    "project:update",
    "deployment:create",
    "deployment:approve",
  ].every(can)
  const selected = target || targets[0]?.id || ""
  async function deploy() {
    if (pending) return
    setPending(true)
    setError("")
    setCreated("")
    try {
      await deployPostgres(
        {
          name: name.trim(),
          target: selected,
          daily: daily && can("service_backup:manage"),
        },
        setProgress,
        (id) => {
          setCreated(id)
          onCreated(id)
        }
      )
      toast.success("PostgreSQL deployed")
    } catch (cause) {
      setError(messageOf(cause, "Deployment could not finish"))
    } finally {
      setPending(false)
    }
  }
  return (
    <section className="rounded-xl border p-4" aria-label="Deploy PostgreSQL">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <DatabaseIcon className="size-5" />
        <h2 className="font-semibold">PostgreSQL 17</h2>
        <Badge variant="secondary">Single instance · Docker</Badge>
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        Private endpoint, restricted application role and a retained local
        volume. Host loss needs a backup stored elsewhere. Point-in-time
        recovery is not available in this profile.
      </p>
      {connections.status === "error" ? (
        <QueryError
          error={connections.error}
          fallback="Could not load targets"
          retry={connections.refetch}
        />
      ) : connections.status === "pending" ? (
        <p role="status">Loading checked targets…</p>
      ) : !targets.length ? (
        <CatalogEmpty
          title="Connect a Docker server"
          description="Add and check an SSH server connection before deploying a database."
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
          <div className="grid min-w-40 flex-1 gap-1.5">
            <Label htmlFor="database-name">Project name</Label>
            <Input
              id="database-name"
              value={name}
              maxLength={100}
              required
              disabled={pending || !allowed}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="grid min-w-48 flex-1 gap-1.5">
            <Label htmlFor="database-target">Docker target</Label>
            <Select
              value={selected}
              disabled={pending || !allowed}
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
          {can("service_backup:manage") && (
            <label className="flex h-9 items-center gap-2 text-sm">
              <Checkbox
                checked={daily}
                onCheckedChange={(value) => setDaily(!!value)}
                disabled={pending}
              />
              Daily backups
            </label>
          )}
          <Button
            type="submit"
            disabled={pending || !allowed || !name.trim() || !selected}
          >
            {pending ? "Deploying…" : "Deploy PostgreSQL"}
          </Button>
        </form>
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
        <Link
          to="/projects/$id"
          params={{ id: created }}
          className="mt-2 inline-block text-sm underline"
        >
          Open service project
        </Link>
      )}
    </section>
  )
}
function ServiceRecovery({ project }: { project: string }) {
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
  const { resources, selected, live, points, copies } = recoverySelection(
    bindings.data?.bindings ?? [],
    backups.data?.backups ?? [],
    restores.data?.restores ?? [],
    environments.data?.environments.find((e) => e.name === environment)?.id,
    resource
  )
  const policy = policies.data?.policies.find(
    (p) => p.environment === environment && p.resource === selected
  )
  const active =
    points.some((b) => ["queued", "running"].includes(b.status)) ||
    copies.some((r) => ["queued", "running", "deleting"].includes(r.status))
  const { refetch: refreshBackups } = backups
  const { refetch: refreshRestores } = restores
  const { refetch: refreshBindings } = bindings
  const { refetch: refreshPolicies } = policies
  // Recovery jobs currently have no realtime event contract. Poll even when the
  // general live connection is healthy; its presence is not evidence of updates.
  useEffect(() => {
    const timer = window.setInterval(
      () => {
        void Promise.allSettled([
          refreshBackups(),
          refreshRestores(),
          refreshBindings(),
          refreshPolicies(),
        ])
      },
      active ? 2000 : 15000
    )
    return () => window.clearInterval(timer)
  }, [
    active,
    refreshBackups,
    refreshRestores,
    refreshBindings,
    refreshPolicies,
  ])
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
      </div>
      {!selected ? (
        <CatalogEmpty
          title="No PostgreSQL service in this environment"
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
            All recovery points are retained.{" "}
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
                    <TableRow key={b.id}>
                      <TableCell className="whitespace-normal">
                        <div>{formatDate(b.createdAt)}</div>
                        <div className="text-xs text-muted-foreground">
                          {b.bytes.toLocaleString()} bytes
                        </div>
                        <div className="mt-1 grid gap-1 text-xs sm:hidden">
                          <Badge variant="secondary">{b.status}</Badge>
                          <span>
                            {b.restoredAt
                              ? `SQL verified ${formatDate(b.restoredAt)}`
                              : "Restore not tested"}
                          </span>
                          {b.error && (
                            <span className="text-destructive">{b.error}</span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <Badge variant="secondary">{b.status}</Badge>
                        {b.error && (
                          <p className="max-w-xs text-xs text-destructive">
                            {b.error}
                          </p>
                        )}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        {b.restoredAt ? (
                          <span>SQL verified {formatDate(b.restoredAt)}</span>
                        ) : (
                          <span className="text-muted-foreground">
                            Not tested
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        {b.status === "succeeded" &&
                          can("service_restore:manage") && (
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={busy}
                              onClick={() =>
                                void action(`restore:${b.id}`, (requestId) =>
                                  createRestore.mutateAsync({
                                    path: { id: b.id },
                                    body: { requestId },
                                  })
                                )
                              }
                            >
                              <RotateCcwIcon />
                              Test restore
                            </Button>
                          )}
                        {b.status === "failed" &&
                          can("service_backup:manage") && (
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={busy}
                              onClick={() =>
                                void action(`retry:${b.id}`, () =>
                                  retryBackup.mutateAsync({
                                    path: { id: b.id },
                                  })
                                )
                              }
                            >
                              Retry backup
                            </Button>
                          )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </>
      )}
      <div className="min-w-0">
        <h3 className="mb-2 text-sm font-semibold">Isolated recovery copies</h3>
        <p className="mb-3 text-sm text-muted-foreground">
          Copies use a private network, never replace a live database, and are
          scheduled for removal after 24 hours. SQL verification does not test
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
                  <TableHead className="hidden sm:table-cell">Status</TableHead>
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
                          SQL verified in {(r.durationMs / 1000).toFixed(1)}s
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
