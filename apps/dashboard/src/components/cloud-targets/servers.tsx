import { useEffect, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Profile,
  Server,
  ServerInputBody,
  Operation,
  DrainReview,
  Connection,
  Provider,
  ServerUsage,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
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
import { Link } from "@/lib/router"
import { QueryError } from "@/components/query-error"
import { useOrganization } from "@/lib/organization"
import { selectClass } from "./workspace"
import {
  CloudEmpty,
  CloudError,
  Field,
  Rows,
  rowClass,
  useInfrastructureRefresh,
} from "./infrastructure-ui"
import { Pools, Recovery } from "./pools"

export function supports(
  providers: Provider[],
  connection: Connection | undefined,
  capability: string
) {
  return (
    !!connection &&
    providers.some(
      (p) =>
        p.available &&
        p.cloud === connection.cloud &&
        p.infrastructure?.includes(capability)
    )
  )
}
export function Servers({ cloud }: { cloud: Profile }) {
  const { can } = useOrganization()
  return can("organization_cloud:manage") ? (
    <ServerContent cloud={cloud} />
  ) : (
    <CloudEmpty title="Server inventory is restricted">
      Your organization manager can review shared workloads and capacity.
    </CloudEmpty>
  )
}
function ServerContent({ cloud }: { cloud: Profile }) {
  const servers = useQuery(api.useCloudServersList, {
    path: { cloudId: cloud.id },
  })
  const { refetch: refetchServers } = servers
  useEffect(() => {
    void refetchServers().catch(() => undefined)
  }, [refetchServers])
  const connections = useQuery(api.useConnectionsList)
  const providers = useQuery(api.useProvidersList)
  const [editing, setEditing] = useState<{
    kind: string
    server?: Server
  } | null>(null)
  const [selected, setSelected] = useState<Server | null>(null)
  const [recovering, setRecovering] = useState<{
    planId: string
    service: string
  } | null>(null)
  const list = servers.data?.servers.filter((s): s is Server => !!s) ?? []
  const create = connections.data?.connections.some(
    (c) =>
      c.status === "ready" &&
      supports(providers.data?.providers ?? [], c, "server:provision")
  )
  return (
    <section className="grid gap-3" aria-label="Servers">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-[15px] font-semibold">{list.length} servers</h2>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            onClick={() => setEditing({ kind: "adopt" })}
          >
            Connect existing server
          </Button>
          {create && (
            <Button onClick={() => setEditing({ kind: "create" })}>
              Create server
            </Button>
          )}
        </div>
      </div>
      {servers.status === "error" ||
      connections.status === "error" ||
      providers.status === "error" ? (
        <QueryError
          error={servers.error ?? connections.error ?? providers.error}
          fallback="Servers couldn't be loaded."
          retry={() =>
            Promise.allSettled([
              servers.refetch(),
              connections.refetch(),
              providers.refetch(),
            ])
          }
        />
      ) : !servers.data || !connections.data || !providers.data ? (
        <p role="status">Loading servers...</p>
      ) : !list.length ? (
        <CloudEmpty title="No servers connected">
          Connect a pinned SSH account, or create a server with an account that
          supports provisioning.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((server) => (
            <ServerRow
              key={server.id}
              cloudId={cloud.id}
              server={server}
              onSelect={() => setSelected(server)}
            />
          ))}
        </Rows>
      )}
      <Pools cloud={cloud} servers={list} />
      {editing && (
        <ServerEditor
          cloud={cloud}
          {...editing}
          providers={providers.data?.providers ?? []}
          connections={connections.data?.connections ?? []}
          onClose={() => setEditing(null)}
          onChanged={() => void servers.refetch()}
        />
      )}
      {selected && (
        <ServerDetails
          cloud={cloud}
          server={list.find((s) => s.id === selected.id) ?? selected}
          capabilities={providers.data?.providers ?? []}
          connections={connections.data?.connections ?? []}
          onClose={() => setSelected(null)}
          onChanged={() => void servers.refetch()}
          onRecover={(planId, service) => {
            setSelected(null)
            setRecovering({ planId, service })
          }}
          onEdit={(kind) => {
            setSelected(null)
            setEditing({ kind, server: selected })
          }}
        />
      )}
      {recovering && (
        <Recovery
          cloudId={cloud.id}
          initialPlan={recovering.planId}
          initialService={recovering.service}
          onClose={() => setRecovering(null)}
        />
      )}
    </section>
  )
}
const gib = (bytes: number) => `${((bytes || 0) / 1073741824).toFixed(1)} GiB`
function Capacity({ server, usage }: { server: Server; usage?: ServerUsage }) {
  return (
    <span className="text-xs text-muted-foreground">
      {usage?.workloads.length ?? "…"} workloads · reserved{" "}
      {usage?.reservedCpuMillis ?? "…"} mCPU /{" "}
      {gib(usage?.reservedMemoryBytes ?? 0)} · available{" "}
      {usage
        ? Math.max(
            0,
            server.health.effectiveCpuMillis - usage.reservedCpuMillis
          )
        : "…"}{" "}
      mCPU /{" "}
      {usage
        ? gib(
            Math.max(
              0,
              server.health.effectiveMemoryBytes - usage.reservedMemoryBytes
            )
          )
        : "…"}
    </span>
  )
}
function ServerRow({
  cloudId,
  server,
  onSelect,
}: {
  cloudId: string
  server: Server
  onSelect: () => void
}) {
  const usage = useQuery(api.useCloudServersUsage, {
    path: { cloudId, serverId: server.id },
  })
  return (
    <div className={rowClass}>
      <div className="grid min-w-0 gap-1">
        <span className="font-medium break-words">
          {server.name}{" "}
          <span className="font-normal text-muted-foreground">
            {server.status}
            {server.draining ? " · draining" : ""}
          </span>
        </span>
        <span className="text-xs text-muted-foreground">
          {server.ownership} · {server.region || "SSH"} ·{" "}
          {server.size || "Detected capacity"} · health checked{" "}
          {server.health.checkedAt || "never"}
        </span>
        {usage.status === "error" ? (
          <span role="alert">Capacity couldn't be loaded.</span>
        ) : (
          <Capacity server={server} usage={usage.data} />
        )}
      </div>
      <Button size="sm" variant="outline" onClick={onSelect}>
        Inspect {server.name}
      </Button>
    </div>
  )
}
function ServerEditor({
  cloud,
  kind,
  server,
  connections,
  providers,
  onClose,
  onChanged,
}: {
  cloud: Profile
  kind: string
  server?: Server
  connections: Connection[]
  providers: Provider[]
  onClose: () => void
  onChanged: () => void
}) {
  const preview = useMutation(api.useCloudServersPreview)
  const apply = useMutation(api.useInfrastructureOperationsApply)
  const [input, setInput] = useState<ServerInputBody>({
    kind,
    cloudRevision: cloud.revision,
    ...(server
      ? {
          serverId: server.id,
          serverRevision: server.revision,
          connectionId: server.connectionId,
          name: server.name,
          region: server.region,
          size: server.size,
        }
      : {}),
  })
  const [operation, setOperation] = useState<Operation | null>(null)
  const [error, setError] = useState("")
  const [accepted, setAccepted] = useState(false)
  const [refresh, setRefresh] = useState(false)
  const busy = preview.isPending || apply.isPending
  const accounts = connections.filter(
    (c) =>
      c.status === "ready" &&
      (kind === "adopt"
        ? c.cloud === "server"
        : supports(
            providers,
            c,
            kind === "resize"
              ? "server:resize"
              : kind === "delete"
                ? "server:delete"
                : "server:provision"
          ))
  )
  const update = (patch: Partial<ServerInputBody>) => {
    setInput({ ...input, ...patch })
    setOperation(null)
  }
  const review = async () => {
    setError("")
    try {
      let next = input
      if (refresh) {
        const current = await api
          .useOrganizationCloudsGet({ path: { cloudId: cloud.id } })
          .refetch()
        const inventory = server
          ? await api
              .useCloudServersList({ path: { cloudId: cloud.id } })
              .refetch()
          : undefined
        const actual = inventory?.servers.find((s) => s?.id === server?.id)
        next = {
          ...input,
          cloudRevision: current.revision,
          ...(actual ? { serverRevision: actual.revision } : {}),
        }
        setInput(next)
      }
      setOperation(
        await preview.mutateAsync({ path: { cloudId: cloud.id }, body: next })
      )
      setRefresh(false)
    } catch (err) {
      setError(messageOf(err, "The server couldn't be reviewed."))
    }
  }
  const execute = async () => {
    if (!operation) return
    setError("")
    try {
      setOperation(
        await apply.mutateAsync({
          path: { cloudId: cloud.id, operationId: operation.id },
          body: { review: operation.review },
        })
      )
      setAccepted(true)
      onChanged()
    } catch (err) {
      setError(messageOf(err, "The server change wasn't accepted."))
      if ([409, 422].includes(statusOf(err) ?? 0)) {
        setOperation(null)
        setRefresh(true)
      }
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {kind === "adopt"
              ? "Connect existing server"
              : `${kind[0]!.toUpperCase()}${kind.slice(1)} server`}
          </DialogTitle>
          <DialogDescription>
            {kind === "disconnect"
              ? "Disconnecting keeps your server. Review workloads and retained cleanup first."
              : kind === "delete"
                ? "Review workloads, routes and retained storage before deleting managed compute."
                : "Review account authority, capacity and cost before applying."}
          </DialogDescription>
        </DialogHeader>
        {!accepted && (
          <div className="grid gap-3">
            {server && <LifecycleUsage cloudId={cloud.id} server={server} />}
            <Field label="Server name">
              <Input
                aria-label="Server name"
                value={input.name ?? ""}
                disabled={busy || !!server}
                onChange={(e) => update({ name: e.target.value })}
              />
            </Field>
            {!server && (
              <Field label="Server account">
                {!accounts.length && (
                  <CloudEmpty
                    title="Connect a server account"
                    action={
                      <Button
                        nativeButton={false}
                        variant="outline"
                        render={<Link to="/integrations" />}
                      >
                        Open Integrations
                      </Button>
                    }
                  >
                    {kind === "adopt"
                      ? "Add a pinned SSH account in Integrations, then return to review this server."
                      : "No ready account supports this provider operation. Check account capabilities in Integrations."}
                  </CloudEmpty>
                )}
                <select
                  aria-label="Server account"
                  className={selectClass}
                  value={input.connectionId ?? ""}
                  onChange={(e) =>
                    update({
                      connectionId: e.target.value,
                      region: "",
                      size: "",
                      image: "",
                    })
                  }
                >
                  <option value="">Choose an account</option>
                  {accounts.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </Field>
            )}
            {["create", "resize"].includes(kind) && input.connectionId && (
              <ServerOptions
                cloudId={cloud.id}
                connectionId={input.connectionId}
                input={input}
                update={update}
              />
            )}
            {kind === "adopt" && (
              <label className="flex items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  checked={input.confirmAdoption ?? false}
                  onChange={(e) =>
                    update({ confirmAdoption: e.target.checked })
                  }
                />
                Confirm pinned SSH access and Docker installation on this
                existing server
              </label>
            )}
            {operation && (
              <div className="grid gap-1 rounded-lg bg-muted p-3 text-sm">
                <p>Review: {operation.status}</p>
                <p>
                  Connection generation:{" "}
                  {operation.connectionGeneration ?? "unavailable"}
                </p>
                <p>
                  Account: {operation.accountId || operation.connectionId} ·
                  ownership: {operation.ownership}
                </p>
                <p>
                  Estimated hourly cost: ${operation.estimatedHourlyUsd ?? 0}
                </p>
                <p>Expires: {operation.expiresAt}</p>
                {operation.actions.map((a, i) => (
                  <p key={i}>
                    {a.kind}: {a.status} {a.resourceId}
                  </p>
                ))}
              </div>
            )}
          </div>
        )}
        {accepted && operation && (
          <div role="status">
            <p>{operation.status}</p>
            <p>{operation.message}</p>
            <p className="text-xs text-muted-foreground">
              Track enrollment, health and cleanup in Activity. Acceptance does
              not establish readiness.
            </p>
          </div>
        )}
        <CloudError message={error} />
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            {accepted ? "Close" : "Cancel"}
          </Button>
          {!accepted &&
            (operation ? (
              <Button
                disabled={busy || operation.status !== "preview"}
                onClick={() => void execute()}
              >
                Apply reviewed server
              </Button>
            ) : (
              <Button
                disabled={
                  busy ||
                  (!server && !input.connectionId) ||
                  (kind === "adopt" && !input.confirmAdoption)
                }
                onClick={() => void review()}
              >
                Review server
              </Button>
            ))}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
function ServerOptions({
  cloudId,
  connectionId,
  input,
  update,
}: {
  cloudId: string
  connectionId: string
  input: ServerInputBody
  update: (p: Partial<ServerInputBody>) => void
}) {
  const options = useQuery(api.useCloudServersOptions, {
    path: { cloudId, connectionId },
  })
  return options.status === "error" ? (
    <QueryError
      error={options.error}
      fallback="Server options couldn't be loaded."
      retry={options.refetch}
    />
  ) : !options.data ? (
    <p role="status">Loading provider options...</p>
  ) : (
    <div className="grid gap-3 sm:grid-cols-2">
      {input.kind === "create" && (
        <Field label="Server region">
          <select
            aria-label="Server region"
            className={selectClass}
            value={input.region ?? ""}
            onChange={(e) => update({ region: e.target.value })}
          >
            <option value="">Choose region</option>
            {options.data.regions.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field label="Server size">
        <select
          aria-label="Server size"
          className={selectClass}
          value={input.size ?? ""}
          onChange={(e) => update({ size: e.target.value })}
        >
          <option value="">Choose size</option>
          {options.data.sizes
            .filter((s) => !input.region || s.regions.includes(input.region))
            .map((s) => (
              <option key={s.id} value={s.id}>
                {s.id} · {s.cpus} CPU · {s.memoryMib} MiB · ${s.hourlyUsd}/h
              </option>
            ))}
        </select>
      </Field>
      {input.kind === "create" && (
        <Field label="Server image">
          <select
            aria-label="Server image"
            className={selectClass}
            value={input.image ?? ""}
            onChange={(e) => update({ image: e.target.value })}
          >
            <option value="">Choose image</option>
            {options.data.images
              .filter((i) => i.bootstrap)
              .map((i) => (
                <option key={i.id} value={i.id}>
                  {i.name}
                </option>
              ))}
          </select>
        </Field>
      )}
    </div>
  )
}
function ServerDetails({
  cloud,
  server,
  capabilities,
  connections,
  onClose,
  onChanged,
  onEdit,
  onRecover,
}: {
  cloud: Profile
  server: Server
  capabilities: Provider[]
  connections: Connection[]
  onClose: () => void
  onChanged: () => void
  onEdit: (kind: string) => void
  onRecover: (planId: string, service: string) => void
}) {
  const usage = useQuery(api.useCloudServersUsage, {
    path: { cloudId: cloud.id, serverId: server.id },
  })
  const preview = useMutation(api.useCloudServersPreviewDrain)
  const apply = useMutation(api.useCloudServersApplyDrain)
  const check = useMutation(api.useCloudServersCheck)
  const [drain, setDrain] = useState<DrainReview | null>(null)
  const [error, setError] = useState("")
  const connection = connections.find((c) => c.id === server.connectionId)
  const busy = preview.isPending || apply.isPending || check.isPending
  async function act(kind: string) {
    setError("")
    try {
      const path = { cloudId: cloud.id, serverId: server.id }
      if (kind === "preview")
        setDrain((await preview.mutateAsync({ path })) as DrainReview)
      else if (kind === "drain" && drain) {
        await apply.mutateAsync({ path, body: { review: drain.review } })
        setDrain(null)
        onChanged()
      } else {
        await check.mutateAsync({ path })
        onChanged()
      }
      await usage.refetch()
    } catch (err) {
      setError(messageOf(err, "The server action failed."))
      setDrain(null)
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{server.name}</DialogTitle>
          <DialogDescription>
            {server.ownership} server · {server.status}. Organization workload
            inventory.
          </DialogDescription>
        </DialogHeader>
        {usage.status === "error" ? (
          <QueryError
            error={usage.error}
            fallback="Workloads couldn't be loaded."
            retry={usage.refetch}
          />
        ) : !usage.data ? (
          <p role="status">Loading workloads...</p>
        ) : (
          <>
            <Capacity server={server} usage={usage.data} />
            <Usage usage={drain?.usage ?? usage.data} onRecover={onRecover} />
            {drain && (
              <p className="text-sm">
                Drain review: {drain.reservations} reservations. New placements
                stop; existing workloads still need reviewed moves.
              </p>
            )}
          </>
        )}
        <CloudError message={error} />
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void act("check")}
          >
            Check health
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void act(drain ? "drain" : "preview")}
          >
            {drain ? "Apply reviewed drain" : "Review drain"}
          </Button>
          {server.ownership === "managed" &&
            supports(capabilities, connection, "server:resize") && (
              <Button variant="outline" onClick={() => onEdit("resize")}>
                Resize
              </Button>
            )}
          {(server.ownership === "adopted" ||
            supports(capabilities, connection, "server:delete")) && (
            <Button
              variant="destructive"
              onClick={() =>
                onEdit(server.ownership === "adopted" ? "disconnect" : "delete")
              }
            >
              {server.ownership === "adopted"
                ? "Review disconnect"
                : "Review deletion"}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
export function Usage({
  usage,
  onRecover,
}: {
  usage: ServerUsage
  onRecover?: (planId: string, service: string) => void
}) {
  return (
    <div className="grid gap-2 text-xs">
      {usage.workloads.map((w, i) => (
        <div key={i} className="rounded-md bg-muted p-2">
          <p>
            {w.projectId} / {w.environmentId} / {w.service}
          </p>
          <p>Saved plan: {w.planId}</p>
          <p>
            {w.phase} · {w.active ? "serving" : "reserved"} · {w.cpuMillis} mCPU
            · {gib(w.memoryBytes)}
          </p>
          {onRecover && !w.active && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => onRecover(w.planId, w.service)}
            >
              Review reservation recovery
            </Button>
          )}
        </div>
      ))}
      {usage.retainedStorage.map((s, i) => (
        <div key={i} className="grid gap-1 text-weld-ink">
          <p>
            Retained storage: {s.projectId} / {s.service}. Saved plan {s.planId}
            . Compute cleanup preserved storage; a named volume is not
            confirmed.
          </p>
          {onRecover && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => onRecover(s.planId, s.service)}
            >
              Review retained cleanup
            </Button>
          )}
        </div>
      ))}
    </div>
  )
}
export function InfrastructureActivity({ cloudId }: { cloudId: string }) {
  const operations = useQuery(api.useInfrastructureOperationsList, {
    path: { cloudId },
  })
  const { refetch: refetchOperations } = operations
  useEffect(() => {
    void refetchOperations().catch(() => undefined)
  }, [refetchOperations])
  const retry = useMutation(api.useInfrastructureOperationsRetry)
  const cancel = useMutation(api.useInfrastructureOperationsCancel)
  const [error, setError] = useState("")
  const list =
    operations.data?.operations.filter((o): o is Operation => !!o) ?? []
  useInfrastructureRefresh(
    list.some((o) => ["pending", "running", "waiting"].includes(o.status)),
    operations.refetch
  )
  async function act(o: Operation, kind: string) {
    setError("")
    try {
      await (kind === "retry" ? retry : cancel).mutateAsync({
        path: { cloudId, operationId: o.id },
      })
      await operations.refetch()
      await api.useCloudServersList({ path: { cloudId } }).refetch()
    } catch (err) {
      setError(messageOf(err, "The operation couldn't be updated."))
    }
  }
  return (
    <section className="grid gap-3" aria-label="Infrastructure activity">
      <h2 className="text-[15px] font-semibold">Infrastructure operations</h2>
      <CloudError message={error} />
      {operations.status === "error" ? (
        <QueryError
          error={operations.error}
          fallback="Operations couldn't be loaded."
          retry={operations.refetch}
        />
      ) : !operations.data ? (
        <p role="status">Loading operations...</p>
      ) : !list.length ? (
        <CloudEmpty title="No infrastructure operations">
          Reviewed server changes and retained cleanup appear here.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((o) => (
            <div key={o.id} className={rowClass}>
              <div className="grid min-w-0 gap-1">
                <p>
                  {o.input.kind} {o.input.name || o.serverId}: {o.status} ·
                  attempt {o.attempt}
                </p>
                <p className="text-xs break-words text-muted-foreground">
                  {o.message}
                </p>
                {o.actions.map((a, i) => (
                  <p key={i} className="text-xs">
                    {a.kind}: {a.status}
                  </p>
                ))}
              </div>
              <div className="flex gap-2">
                {["failed", "waiting", "cleanup_pending"].includes(
                  o.status
                ) && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={retry.isPending}
                    onClick={() => void act(o, "retry")}
                  >
                    Retry operation
                  </Button>
                )}
                {!o.attempted && ["preview", "pending"].includes(o.status) && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={cancel.isPending}
                    onClick={() => void act(o, "cancel")}
                  >
                    Cancel operation
                  </Button>
                )}
              </div>
            </div>
          ))}
        </Rows>
      )}
    </section>
  )
}

function LifecycleUsage({
  cloudId,
  server,
}: {
  cloudId: string
  server: Server
}) {
  const usage = useQuery(api.useCloudServersUsage, {
    path: { cloudId, serverId: server.id },
  })
  return usage.status === "error" ? (
    <QueryError
      error={usage.error}
      fallback="Affected workloads couldn't be loaded. Resolve this before reviewing removal."
      retry={usage.refetch}
    />
  ) : !usage.data ? (
    <p role="status">Loading affected workloads...</p>
  ) : (
    <div className="grid gap-2">
      <Usage usage={usage.data} />
      {[...new Set(usage.data.workloads.map((w) => w.projectId))].map(
        (projectId) => (
          <AffectedRoutes
            key={projectId}
            projectId={projectId}
            serverId={server.id}
          />
        )
      )}
    </div>
  )
}
function AffectedRoutes({
  projectId,
  serverId,
}: {
  projectId: string
  serverId: string
}) {
  const routes = useQuery(api.useDomainRoutesList, { path: { projectId } })
  return routes.status === "error" ? (
    <QueryError
      error={routes.error}
      fallback="Affected project routes couldn't be loaded."
      retry={routes.refetch}
    />
  ) : (
    <div className="grid gap-1 text-xs">
      {routes.data?.routes
        .filter(
          (r) => r?.binding.serverId === serverId && r.status !== "removed"
        )
        .map(
          (r) =>
            r && (
              <p key={r.id}>
                Route: {r.input.hostname} · {r.input.service} · {r.status}
              </p>
            )
        )}
    </div>
  )
}
