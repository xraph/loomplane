import { PlusIcon } from "lucide-react"
import { ClusterMoney } from "@/components/cloud-detail/cluster-money"
import {
  ActivityIcon as ActivityPulseIcon,
  InfrastructureIcon,
  RestrictedIcon,
} from "@/components/icons"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
  RowMenu,
  hourlyCost,
} from "@/components/cloud-targets/cloud-page"
import { CloudLoading } from "@/components/cloud-targets/infrastructure-ui"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
} from "@/components/ui/select"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { Checkbox } from "@/components/ui/checkbox"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { useEffect, useRef, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Profile,
  Server,
  ServerInputBody,
  Operation,
  DrainReview,
  Connection,
  Node,
  Provider,
  ServerUsage,
  ServerImage,
  ServerSize,
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
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import { useNow } from "@/lib/use-runs"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { NodeStatus } from "@/components/nodes/node-status"
import { Badge } from "@/components/ui/badge"
import { nodeIdOf } from "@/lib/nodes"
import { useOrganization } from "@/lib/organization"
import {
  acceleratorLabel,
  formatHourly,
  formatMonthly,
  hourlyOrUnknown,
  priceKnown,
  serverGpuLabel,
  serverHourly,
} from "@/lib/gpu"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  Field,
  Row,
  Rows,
  StatePill,
  statusWord,
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
/** The lists Servers can show; with none it shows both. */
export type ServersView = "servers" | "pools"

/**
 * Servers is a Cloud's server inventory and the pools built from it. The
 * Infrastructure tab shows one list at a time; with no view both stack.
 */
export function Servers({
  cloud,
  view,
}: {
  cloud: Profile
  view?: ServersView
}) {
  const { can } = useOrganization()
  return can("organization_cloud:manage") ? (
    <ServerContent cloud={cloud} view={view} />
  ) : (
    <CloudEmpty
      kind="notice"
      icon={RestrictedIcon}
      title="Server inventory is restricted"
    >
      Your workspace manager can review shared workloads and capacity.
    </CloudEmpty>
  )
}

/** NodeVia is the node a server runs on, once that node has loaded. */
export type NodeVia = { id: string; node?: Node }

/** A server action that opens the details dialog and starts there. */
type ServerAction = "check" | "preview"

const serverColumns =
  "minmax(0,1.3fr) minmax(0,1.1fr) minmax(0,1.3fr) 150px 130px"

function ServerContent({
  cloud,
  view,
}: {
  cloud: Profile
  view?: ServersView
}) {
  const servers = useQuery(api.useCloudServersList, {
    path: { cloudId: cloud.id },
  })
  const { refetch: refetchServers } = servers
  useEffect(() => {
    void refetchServers().catch(() => undefined)
  }, [refetchServers])
  const connections = useQuery(api.useConnectionsList)
  const providers = useQuery(api.useProvidersList)
  const pools = useQuery(api.useCloudPoolsList, { path: { cloudId: cloud.id } })
  // A node server's state is its node's. Someone who can't read nodes still
  // sees the server, just without the node's own words.
  const nodes = useQuery(api.useNodesList)
  const [editing, setEditing] = useState<{
    kind: string
    server?: Server
  } | null>(null)
  const [selected, setSelected] = useState<{
    server: Server
    action?: ServerAction
  } | null>(null)
  const [recovering, setRecovering] = useState<{
    planId: string
    service: string
  } | null>(null)
  const list = servers.data?.servers.filter((s): s is Server => !!s) ?? []
  const capabilities = providers.data?.providers ?? []
  const accounts = connections.data?.connections ?? []
  const create = accounts.some(
    (c) => c.status === "ready" && supports(capabilities, c, "server:provision")
  )
  const nodeOf = (server: Server): NodeVia | undefined => {
    const id = nodeIdOf(
      accounts.find(
        (c) =>
          c.id === server.connectionId || c.id === server.runtimeConnectionId
      )
    )
    return id
      ? { id, node: nodes.data?.nodes.find((n) => n.id === id) }
      : undefined
  }
  const poolOf = (serverId: string) =>
    pools.data?.pools.find((p) =>
      p?.members.some((m) => m.serverId === serverId)
    )?.name
  const showServers = view !== "pools"
  const showPools = view !== "servers"
  const actions = (
    <>
      <Button
        size="sm"
        variant="outline"
        onClick={() => setEditing({ kind: "adopt" })}
      >
        Connect server
      </Button>
      {create && (
        <Button size="sm" onClick={() => setEditing({ kind: "create" })}>
          <PlusIcon />
          Create server
        </Button>
      )}
    </>
  )
  return (
    <div className="grid gap-6">
      {showServers && (
        <section className="grid gap-2.5" aria-label="Servers">
          <CloudSectionHead
            title="Servers"
            count={list.length}
            description="Machines that server-pool targets run on. Connect one you already have, or create one through an account that can."
          >
            {actions}
          </CloudSectionHead>
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
            <Skeleton className="h-32 rounded-xl" />
          ) : !list.length ? (
            <CloudEmpty
              kind="list"
              icon={InfrastructureIcon}
              title="No servers yet"
              action={actions}
            >
              Connect a server over a pinned SSH account, or create one with an
              account that can provision.
            </CloudEmpty>
          ) : (
            <CloudTable
              label="Servers"
              columns={serverColumns}
              head={["Server", "Where", "Reserved", "Status", ""]}
              minWidth={760}
            >
              {list.map((server) => {
                const connection = accounts.find(
                  (c) => c.id === server.connectionId
                )
                return (
                  <ServerRow
                    key={server.id}
                    cloudId={cloud.id}
                    server={server}
                    via={nodeOf(server)}
                    pool={poolOf(server.id)}
                    canResize={
                      server.ownership === "managed" &&
                      supports(capabilities, connection, "server:resize")
                    }
                    canRemove={
                      server.ownership === "adopted" ||
                      supports(capabilities, connection, "server:delete")
                    }
                    onOpen={(action) => setSelected({ server, action })}
                    onEdit={(kind) => setEditing({ kind, server })}
                  />
                )
              })}
            </CloudTable>
          )}
        </section>
      )}
      {showPools && (
        <Pools cloud={cloud} servers={list} primary={view === "pools"} />
      )}
      {editing && (
        <ServerEditor
          cloud={cloud}
          {...editing}
          providers={capabilities}
          connections={accounts}
          onClose={() => setEditing(null)}
          onChanged={() => void servers.refetch()}
        />
      )}
      {selected && (
        <ServerDetails
          cloud={cloud}
          server={
            list.find((s) => s.id === selected.server.id) ?? selected.server
          }
          via={nodeOf(selected.server)}
          action={selected.action}
          capabilities={capabilities}
          connections={accounts}
          onClose={() => setSelected(null)}
          onChanged={() => void servers.refetch()}
          onRecover={(planId, service) => {
            setSelected(null)
            setRecovering({ planId, service })
          }}
          onEdit={(kind) => {
            setSelected(null)
            setEditing({ kind, server: selected.server })
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
    </div>
  )
}
const gib = (bytes: number) => `${Number(((bytes || 0) / 2 ** 30).toFixed(1))}`
const cpus = (millis: number) => `${Number(((millis || 0) / 1000).toFixed(2))}`
/** reservedLine is a server's reserved share, as "x of y vCPU · a of b GiB". */
function reservedLine(server: Server, usage: ServerUsage) {
  const gpus = gpuCount(server)
  return [
    `${cpus(usage.reservedCpuMillis)} of ${cpus(server.health.effectiveCpuMillis)} vCPU`,
    `${gib(usage.reservedMemoryBytes)} of ${gib(server.health.effectiveMemoryBytes)} GiB`,
    gpus > 0 && `${usage.reservedGpus} of ${gpus} GPUs`,
  ]
    .filter(Boolean)
    .join(" · ")
}
function Capacity({
  server,
  usage,
  services,
}: {
  server: Server
  usage?: ServerUsage
  /** Also count the services the reservations belong to. */
  services?: boolean
}) {
  const total = server.health.effectiveCpuMillis
  // A checked host that reports no capacity, such as one on cgroup v1, is
  // unknown, not empty.
  if (
    total === 0 &&
    server.health.effectiveMemoryBytes === 0 &&
    server.health.checkedAt
  )
    return (
      <span className="text-xs text-muted-foreground">Capacity unknown</span>
    )
  const reserved = usage?.reservedCpuMillis ?? 0
  const share = total > 0 ? Math.min(100, (reserved / total) * 100) : 0
  return (
    <span className="grid max-w-md min-w-0 gap-1.5 text-xs text-muted-foreground">
      <span className="flex flex-wrap justify-between gap-x-3">
        <span className="tabular-nums">
          {usage ? reservedLine(server, usage) : "Loading capacity…"}
        </span>
        {services && usage && (
          <span>
            {usage.workloads.length}{" "}
            {usage.workloads.length === 1 ? "service" : "services"}
          </span>
        )}
      </span>
      <span
        role="meter"
        aria-label={`${server.name} CPU reserved`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(share)}
        className="flex h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <span
          className={share > 85 ? "bg-failed" : "bg-foreground/70"}
          style={{ width: `${share}%` }}
        />
      </span>
    </span>
  )
}
// How many GPUs a server has: its listed devices, else what its size bought.
function gpuCount(server: Server) {
  return server.health.gpus?.length || server.accelerator?.count || 0
}
// A GPU server whose runtime isn't ready can't run GPU containers. The
// inventory's own message says why.
function gpuProblem(server: Server) {
  if (!gpuCount(server) || server.health.gpuRuntime === "ready") return null
  return (
    server.health.gpuMessage ||
    `GPU status: ${server.health.gpuRuntime || "not checked yet"}.`
  )
}
function GpuSummary({ server }: { server: Server }) {
  const hourly = serverHourly(server)
  return (
    <>
      {serverGpuLabel(server)} ·{" "}
      {hourly === undefined
        ? formatHourly(0, false)
        : `${formatHourly(hourly, true)}, ${formatMonthly(hourly)} while it runs`}
    </>
  )
}
/** ownedBy says who runs a server, never its raw ownership key. */
const ownedBy = (server: Server, via?: NodeVia) =>
  server.ownership === "managed"
    ? "Created by Loomplane"
    : via
      ? "Connected by its owner"
      : "Connected over SSH"
function ServerRow({
  cloudId,
  server,
  via,
  pool,
  canResize,
  canRemove,
  onOpen,
  onEdit,
}: {
  cloudId: string
  server: Server
  via?: NodeVia
  pool?: string
  canResize: boolean
  canRemove: boolean
  onOpen: (action?: ServerAction) => void
  onEdit: (kind: string) => void
}) {
  const usage = useQuery(api.useCloudServersUsage, {
    path: { cloudId, serverId: server.id },
  })
  const problem = gpuProblem(server)
  const now = useNow()
  return (
    <CloudTableRow>
      <NameCell
        mono
        tile={<InfrastructureIcon />}
        name={server.name}
        meta={[
          server.size || "Detected capacity",
          gpuCount(server) > 0 &&
            `${serverGpuLabel(server)} · ${hourlyOrUnknown(serverHourly(server), true)}`,
          pool ?? "No pool",
        ]
          .filter(Boolean)
          .join(" · ")}
      />
      <Cell>
        <span className="grid gap-px">
          <span className="truncate text-[12.5px]">
            {[
              server.ownership === "managed"
                ? cloudLabel(server.provider)
                : "Your server",
              server.region,
            ]
              .filter(Boolean)
              .join(" · ")}
          </span>
          <span className="truncate text-xs text-muted-foreground">
            {ownedBy(server, via)} ·{" "}
            {server.health.checkedAt
              ? `checked ${relative(Date.parse(server.health.checkedAt), now)}`
              : "never checked"}
          </span>
        </span>
      </Cell>
      <Cell>
        {usage.status === "error" ? (
          <span className="text-xs text-destructive">
            Capacity couldn't be loaded.
          </span>
        ) : (
          <Capacity server={server} usage={usage.data} />
        )}
      </Cell>
      <Cell>
        <span className="flex flex-wrap gap-1">
          {via && <Badge variant="secondary">Node</Badge>}
          {via?.node && ["offline", "revoked"].includes(via.node.status) ? (
            <NodeStatus node={via.node} />
          ) : (
            <StatePill status={server.draining ? "draining" : server.status} />
          )}
          {problem && (
            <span title={problem}>
              <StatusPill tone="waiting">No GPU runtime</StatusPill>
            </span>
          )}
        </span>
      </Cell>
      <ActionsCell>
        <Button
          size="sm"
          variant="outline"
          aria-label={`Open ${server.name}`}
          onClick={() => onOpen()}
        >
          Open
        </Button>
        <RowMenu label={`More actions for ${server.name}`}>
          <DropdownMenuItem onClick={() => onOpen("check")}>
            Check health
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => onOpen("preview")}>
            Review drain
          </DropdownMenuItem>
          {canResize && (
            <DropdownMenuItem onClick={() => onEdit("resize")}>
              Resize
            </DropdownMenuItem>
          )}
          {canRemove && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onClick={() =>
                  onEdit(
                    server.ownership === "adopted" ? "disconnect" : "delete"
                  )
                }
              >
                {server.ownership === "adopted"
                  ? "Review disconnect"
                  : "Review deletion"}
              </DropdownMenuItem>
            </>
          )}
        </RowMenu>
      </ActionsCell>
    </CloudTableRow>
  )
}
export function ServerEditor({
  cloud,
  kind,
  server,
  initial,
  connections,
  providers,
  onClose,
  onChanged,
}: {
  cloud: Profile
  kind: string
  server?: Server
  /** Fields to start the review with, such as the node being adopted. */
  initial?: Partial<ServerInputBody>
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
    ...initial,
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
              ? "Connect a server you already run"
              : kind === "create"
                ? "Create a server"
                : `${kind[0]!.toUpperCase()}${kind.slice(1)} ${server?.name ?? "server"}`}
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
                    kind="inline"
                    icon={InfrastructureIcon}
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
                <Select
                  value={input.connectionId ?? ""}
                  onValueChange={(e) =>
                    update({
                      connectionId: e ?? "",
                      region: "",
                      size: "",
                      image: "",
                    })
                  }
                >
                  <SelectTrigger
                    aria-label="Server account"
                    className={"w-full min-w-0"}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">Choose an account</SelectItem>
                    {accounts.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
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
              <FormField
                orientation="horizontal"
                className="flex items-center gap-2 text-xs"
              >
                <FieldLabel className="contents">
                  <Checkbox
                    checked={input.confirmAdoption ?? false}
                    onCheckedChange={(e) => update({ confirmAdoption: e })}
                  />
                  {connections.find((c) => c.id === input.connectionId)
                    ?.method === "node"
                    ? "Confirm Docker on this node's machine as a server of this Cloud"
                    : "Confirm pinned SSH access and Docker installation on this existing server"}
                </FieldLabel>
              </FormField>
            )}
            {operation &&
              (operation.budgetReview?.estimate || operation.moneyEstimate) && (
                <ClusterMoney
                  estimate={
                    operation.budgetReview?.estimate ?? operation.moneyEstimate
                  }
                />
              )}
            {operation &&
              !(operation.budgetReview?.estimate ?? operation.moneyEstimate)
                ?.known &&
              (operation.budgetReview || operation.moneyEstimate) && (
                <label className="flex items-start gap-2 text-sm">
                  <Checkbox
                    aria-label="Acknowledge the displayed unknown cost categories"
                    checked={input.acknowledgeUnknownCosts ?? false}
                    onCheckedChange={(v) => {
                      setInput({
                        ...input,
                        acknowledgeUnknownCosts: v === true,
                      })
                      setOperation(null)
                    }}
                  />
                  <span>
                    Acknowledge the displayed unknown cost categories for this
                    manual review, then review again.
                  </span>
                </label>
              )}
            {operation && (
              <FactList
                facts={[
                  {
                    label: "Review",
                    value: <StatePill status={operation.status} />,
                  },
                  {
                    label: "Connection generation",
                    value: operation.connectionGeneration ?? "Unavailable",
                  },
                  {
                    label: "Account",
                    value: (
                      <span className="break-all">
                        {operation.accountId || operation.connectionId}
                      </span>
                    ),
                  },
                  ...(operation.ownership
                    ? [
                        {
                          label: "Ownership",
                          value:
                            operation.ownership === "managed"
                              ? "Created by Loomplane"
                              : "Connected over SSH",
                        },
                      ]
                    : []),
                  ...(["create", "resize"].includes(operation.input.kind)
                    ? [{ label: "Cost", value: estimatedCost(operation) }]
                    : []),
                  ...(operation.expiresAt
                    ? [
                        {
                          label: "Review expires",
                          value: formatDate(operation.expiresAt),
                        },
                      ]
                    : []),
                  ...operation.actions.map((a, i) => ({
                    label: `Step ${i + 1}`,
                    value: (
                      <span className="break-all">
                        {a.kind} · {statusWord(a.status)}
                        {a.resourceId && ` · ${a.resourceId}`}
                      </span>
                    ),
                  })),
                ]}
              />
            )}
          </div>
        )}
        {accepted && operation && (
          <div role="status" className="grid gap-2 text-[13px]">
            <StatePill status={operation.status} />
            <p>{operation.message}</p>
            <p className="text-[12.5px] text-muted-foreground">
              Track enrollment, health and cleanup on the Changes tab. An
              accepted change isn't ready yet.
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
                disabled={
                  busy ||
                  operation.status !== "preview" ||
                  (!!operation.budgetReview &&
                    !operation.budgetReview.estimate.known &&
                    !operation.input.acknowledgeUnknownCosts)
                }
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
  const sizes =
    options.data?.sizes.filter(
      (s) => !input.region || s.regions.includes(input.region)
    ) ?? []
  return options.status === "error" ? (
    <QueryError
      error={options.error}
      fallback="Server options couldn't be loaded."
      retry={options.refetch}
    />
  ) : !options.data ? (
    <CloudLoading>Loading provider options...</CloudLoading>
  ) : (
    <div className="grid gap-3 sm:grid-cols-2">
      {input.kind === "create" && (
        <Field label="Server region">
          <Select
            value={input.region ?? ""}
            onValueChange={(e) => update({ region: e ?? "" })}
          >
            <SelectTrigger
              aria-label="Server region"
              className={"w-full min-w-0"}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Choose region</SelectItem>
              {options.data.regions.map((r) => (
                <SelectItem key={r.id} value={r.id}>
                  {r.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      <Field label="Server size">
        <Select
          value={input.size ?? ""}
          onValueChange={(e) => {
            const size = sizes.find((s) => s.id === e)
            const fit = imagesFor(options.data?.images ?? [], size)
            update({
              size: e ?? "",
              ...(input.kind === "create" &&
              !fit.some((i) => i.id === input.image)
                ? { image: size?.accelerator ? (fit[0]?.id ?? "") : "" }
                : {}),
            })
          }}
        >
          <SelectTrigger aria-label="Server size" className={"w-full min-w-0"}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">Choose size</SelectItem>
            {sizes
              .filter((s) => !s.accelerator)
              .map((s) => (
                <SelectItem key={s.id} value={s.id}>
                  {`${s.id} · ${s.cpus} vCPU · ${Number((s.memoryMib / 1024).toFixed(1))} GiB · ${hourlyOrUnknown(s.hourlyUsd, s.hourlyPriceKnown)}`}
                </SelectItem>
              ))}
            {sizes.some((s) => s.accelerator) && (
              <SelectGroup>
                <SelectLabel>GPU</SelectLabel>
                {sizes
                  .filter((s) => s.accelerator)
                  .map((s) => (
                    <SelectItem key={s.id} value={s.id}>
                      {gpuSizeLabel(s)}
                    </SelectItem>
                  ))}
              </SelectGroup>
            )}
          </SelectContent>
        </Select>
      </Field>
      {input.kind === "create" && (
        <Field label="Server image">
          <Select
            value={input.image ?? ""}
            onValueChange={(e) => update({ image: e ?? "" })}
          >
            <SelectTrigger
              aria-label="Server image"
              className={"w-full min-w-0"}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Choose image</SelectItem>
              {imagesFor(
                options.data.images,
                sizes.find((s) => s.id === input.size)
              ).map((i) => (
                <SelectItem key={i.id} value={i.id}>
                  {i.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    </div>
  )
}
// A GPU size reads "gpu-h100x1-80gb · 1× NVIDIA H100 80GB · 80 GiB · 20 vCPU ·
// $3.39/hr", with the memory each GPU has.
function gpuSizeLabel(s: ServerSize) {
  const a = s.accelerator!
  return `${s.id} · ${acceleratorLabel(a)} · ${a.memoryGib} GiB · ${s.cpus} vCPU · ${hourlyOrUnknown(s.hourlyUsd, s.hourlyPriceKnown)}`
}
// A GPU size boots only an image that ships its vendor's drivers, and a CPU
// size never needs one.
function imagesFor(images: ServerImage[], size?: ServerSize) {
  const vendor = size?.accelerator?.vendor
  return images.filter(
    (i) =>
      i.bootstrap &&
      (vendor
        ? (i.accelerators ?? []).includes(vendor)
        : !i.accelerators?.length)
  )
}
function estimatedCost(o: Operation) {
  const known = priceKnown(o.estimatedHourlyUsd, o.hourlyPriceKnown)
  if (o.accelerator)
    return known
      ? `Estimated cost: ${formatHourly(o.estimatedHourlyUsd, true)}, ${formatMonthly(o.estimatedHourlyUsd)} while it runs`
      : `Estimated cost: ${formatHourly(0, false)}`
  return known
    ? `Estimated cost: ${hourlyCost(o.estimatedHourlyUsd)}`
    : `Estimated cost: ${formatHourly(0, false)}`
}
function ServerDetails({
  cloud,
  server,
  via,
  action,
  capabilities,
  connections,
  onClose,
  onChanged,
  onEdit,
  onRecover,
}: {
  cloud: Profile
  server: Server
  via?: NodeVia
  /** An action a row's menu started, run once when the dialog opens. */
  action?: ServerAction
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
  const started = useRef(false)
  useEffect(() => {
    if (!action || started.current) return
    started.current = true
    void act(action)
    // Run the row's action once, when the dialog opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [action])
  const problem = gpuProblem(server)
  const removable =
    server.ownership === "adopted" ||
    supports(capabilities, connection, "server:delete")
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{server.name}</DialogTitle>
          <DialogDescription>
            {ownedBy(server, via)} · {statusWord(server.status)}. What runs on
            this server and what it has reserved.
          </DialogDescription>
        </DialogHeader>
        {via && (
          <p className="text-[13px]">
            Runs on the node{" "}
            <Link
              to="/nodes/$id"
              params={{ id: via.id }}
              className="font-medium underline underline-offset-2"
            >
              {via.node?.name ?? via.id}
            </Link>
            {via.node && (
              <>
                {" "}
                <NodeStatus node={via.node} />
              </>
            )}
          </p>
        )}
        {gpuCount(server) > 0 && (
          <div className="grid gap-2 text-[13px]">
            <p>
              <GpuSummary server={server} />
            </p>
            {problem && (
              <CloudCallout title="No GPU runtime">{problem}</CloudCallout>
            )}
          </div>
        )}
        {usage.status === "error" ? (
          <QueryError
            error={usage.error}
            fallback="Workloads couldn't be loaded."
            retry={usage.refetch}
          />
        ) : !usage.data ? (
          <Skeleton className="h-32 rounded-xl" />
        ) : (
          <>
            <Capacity server={server} usage={usage.data} services />
            <Usage
              usage={drain?.usage ?? usage.data}
              onRecover={onRecover}
              label={`Workloads on ${server.name}`}
            />
            {drain && (
              <p className="text-[13px]">
                Drain review: {drain.reservations} reservations. New placements
                stop; existing workloads still need reviewed moves.
              </p>
            )}
          </>
        )}
        <CloudError message={error} />
        <DialogFooter>
          {removable && (
            <Button
              variant="destructive"
              className="mr-auto"
              onClick={() =>
                onEdit(server.ownership === "adopted" ? "disconnect" : "delete")
              }
            >
              {server.ownership === "adopted"
                ? "Review disconnect"
                : "Review deletion"}
            </Button>
          )}
          {server.ownership === "managed" &&
            supports(capabilities, connection, "server:resize") && (
              <Button variant="outline" onClick={() => onEdit("resize")}>
                Resize
              </Button>
            )}
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void act("check")}
          >
            Check health
          </Button>
          <Button
            variant={drain ? "default" : "outline"}
            disabled={busy}
            onClick={() => void act(drain ? "drain" : "preview")}
          >
            {drain ? "Apply reviewed drain" : "Review drain"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
const usageColumns = "minmax(0,1.5fr) 130px minmax(0,1fr) 190px"
/**
 * Usage lists the workloads a server has reserved room for, and storage a
 * cleanup kept, each with its recovery review.
 */
export function Usage({
  usage,
  onRecover,
  label = "Workloads",
}: {
  usage: ServerUsage
  onRecover?: (planId: string, service: string) => void
  label?: string
}) {
  if (!usage.workloads.length && !usage.retainedStorage.length) return null
  return (
    <CloudTable
      label={label}
      columns={usageColumns}
      head={["Service", "State", "Reserved", ""]}
      minWidth={560}
    >
      {usage.workloads.map((w, i) => (
        <CloudTableRow key={`w${i}`}>
          <NameCell
            name={w.service}
            meta={`${w.projectId} / ${w.environmentId} · plan ${w.planId}`}
          />
          <Cell>
            <span className="grid gap-1">
              <StatePill status={w.phase} />
              <span className="text-xs text-muted-foreground">
                {w.active ? "Serving" : "Reserved only"}
              </span>
            </span>
          </Cell>
          <Cell className="text-[12.5px] tabular-nums">
            {cpus(w.cpuMillis)} vCPU · {gib(w.memoryBytes)} GiB
          </Cell>
          <ActionsCell>
            {onRecover && !w.active && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => onRecover(w.planId, w.service)}
              >
                Review reservation recovery
              </Button>
            )}
          </ActionsCell>
        </CloudTableRow>
      ))}
      {usage.retainedStorage.map((r, i) => (
        <CloudTableRow key={`s${i}`}>
          <NameCell
            name={r.service}
            meta={`${r.projectId} · plan ${r.planId}`}
          />
          <Cell>
            <StatusPill tone="waiting">Storage kept</StatusPill>
          </Cell>
          <Cell className="text-xs text-muted-foreground">
            Compute cleanup kept storage; a named volume isn't confirmed.
          </Cell>
          <ActionsCell>
            {onRecover && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => onRecover(r.planId, r.service)}
              >
                Review retained cleanup
              </Button>
            )}
          </ActionsCell>
        </CloudTableRow>
      ))}
    </CloudTable>
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
      <CloudSectionHead
        title="Infrastructure operations"
        description="Changes Loomplane made to this Cloud's servers, and any you can retry."
      />
      <CloudError message={error} />
      {operations.status === "error" ? (
        <QueryError
          error={operations.error}
          fallback="Operations couldn't be loaded."
          retry={operations.refetch}
        />
      ) : !operations.data ? (
        <CloudLoading>Loading operations...</CloudLoading>
      ) : !list.length ? (
        <CloudEmpty
          kind="list"
          icon={ActivityPulseIcon}
          title="No infrastructure operations"
        >
          Reviewed server changes and retained cleanup appear here.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((o) => (
            <Row key={o.id}>
              <div className="grid min-w-0 flex-1 gap-1">
                <p className="flex flex-wrap items-center gap-2">
                  <span className="text-[13px] font-medium">
                    {o.input.kind} {o.input.name || o.serverId}
                  </span>
                  <StatePill status={o.status} />
                  <span className="text-xs text-muted-foreground">
                    attempt {o.attempt}
                  </span>
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
            </Row>
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
    <Skeleton className="h-24 rounded-xl" />
  ) : (
    <div className="grid gap-2">
      <Usage usage={usage.data} label={`Workloads on ${server.name}`} />
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
