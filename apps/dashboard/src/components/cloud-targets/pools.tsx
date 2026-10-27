import { EmptyIcon, PoolsIcon, ProjectsIcon } from "@/components/icons"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { CloudLoading } from "@/components/cloud-targets/infrastructure-ui"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Checkbox } from "@/components/ui/checkbox"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Profile,
  Server,
  Pool,
  PoolInputBody,
  MoveInputBody,
  MoveReview,
  ReservationRecovery,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { Link } from "@/lib/router"
import { messageOf } from "@/client/errors"
import { gpuTitle, gpusUnread, serverGpusOfKind } from "@/lib/gpu"
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
import { QueryError } from "@/components/query-error"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  Field,
  Row,
  Rows,
  SetupReads,
} from "./infrastructure-ui"

export function Pools({
  cloud,
  servers,
}: {
  cloud: Profile
  servers: Server[]
}) {
  const pools = useQuery(api.useCloudPoolsList, { path: { cloudId: cloud.id } })
  const [editing, setEditing] = useState<Pool | "new" | null>(null)
  const [moving, setMoving] = useState<Pool | null>(null)
  const [recovering, setRecovering] = useState(false)
  const list = pools.data?.pools.filter((p): p is Pool => !!p) ?? []
  // A ready server whose GPUs can't be counted can hold back GPU growth.
  const unread = list.some((p) => p.acceleratorKind)
    ? servers.filter(gpusUnread).map((s) => s.name)
    : []
  return (
    <section className="grid gap-3" aria-label="Server pools">
      <CloudSectionHead
        title="Pools and reservations"
        count={pools.data ? list.length : undefined}
        description="Groups of ready servers, each with the CPU and memory it can give to services."
      >
        <>
          {servers.length > 0 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => setRecovering(true)}
            >
              Recover reservation
            </Button>
          )}
          <Button
            size="sm"
            variant="outline"
            disabled={!servers.some((s) => s.status === "ready")}
            onClick={() => setEditing("new")}
          >
            Add pool
          </Button>
        </>
      </CloudSectionHead>
      {unread.length > 0 && (
        <Alert>
          <AlertDescription>
            {`The GPUs on ${unread.join(", ")} couldn't be read. Loomplane may not add GPU servers to this Cloud until a check succeeds.`}
          </AlertDescription>
        </Alert>
      )}
      {pools.status === "error" ? (
        <QueryError
          error={pools.error}
          fallback="Pools couldn't be loaded."
          retry={pools.refetch}
        />
      ) : !pools.data ? (
        <CloudLoading>Loading pools...</CloudLoading>
      ) : !list.length ? (
        <CloudEmpty kind="list" icon={PoolsIcon} title="No server pools">
          Add ready servers to a pool and choose their usable CPU and memory
          budgets.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((p) => (
            <Row key={p.id}>
              <div>
                <p className="font-mono text-[13px] font-medium">{p.name}</p>
                <p className="text-xs text-muted-foreground">
                  {p.members.length} members ·{" "}
                  {p.members.filter((m) => m.draining).length} draining ·
                  revision {p.revision}
                  {p.acceleratorKind &&
                    ` · ${p.members.reduce((n, m) => n + (m.gpus ?? 0), 0)} ${gpuTitle(p.acceleratorKind)} GPUs`}
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setEditing(p)}
                >
                  Edit pool
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setMoving(p)}
                >
                  Review service move
                </Button>
              </div>
            </Row>
          ))}
        </Rows>
      )}
      {editing && (
        <PoolEditor
          cloudId={cloud.id}
          pool={editing === "new" ? undefined : editing}
          servers={servers}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void pools.refetch()
          }}
        />
      )}
      {moving && (
        <MoveEditor
          cloudId={cloud.id}
          pool={moving}
          servers={servers}
          onClose={() => setMoving(null)}
        />
      )}
      {recovering && (
        <Recovery cloudId={cloud.id} onClose={() => setRecovering(false)} />
      )}
    </section>
  )
}
function PoolEditor({
  cloudId,
  pool,
  servers,
  onClose,
  onSaved,
}: {
  cloudId: string
  pool?: Pool
  servers: Server[]
  onClose: () => void
  onSaved: () => void
}) {
  const create = useMutation(api.useCloudPoolsCreate)
  const update = useMutation(api.useCloudPoolsUpdate)
  const [input, setInput] = useState<PoolInputBody>({
    name: pool?.name ?? "",
    revision: pool?.revision ?? 0,
    members: pool?.members ?? [],
    ...(pool?.acceleratorKind ? { acceleratorKind: pool.acceleratorKind } : {}),
  })
  const kind = input.acceleratorKind ?? ""
  const memberServers = servers.filter((s) =>
    input.members.some((m) => m.serverId === s.id)
  )
  const kinds = [
    ...new Set([
      ...memberServers.flatMap((s) =>
        (s.health.gpus ?? []).map((d) => d.kind).filter(Boolean)
      ),
      ...(kind ? [kind] : []),
    ]),
  ].sort()
  // A GPU pool holds only servers with its kind, each with a GPU budget no
  // bigger than what it has. A CPU pool holds no GPU budgets.
  const chooseKind = (next: string) =>
    setInput({
      ...input,
      acceleratorKind: next || undefined,
      members: next
        ? input.members.flatMap((m) => {
            const server = servers.find((s) => s.id === m.serverId)
            const have = server ? serverGpusOfKind(server, next) : 0
            if (!have) return []
            return [{ ...m, gpus: m.gpus && m.gpus <= have ? m.gpus : have }]
          })
        : input.members.map((m) => ({ ...m, gpus: undefined })),
    })
  const gpusOf = (serverId: string) => {
    const server = servers.find((s) => s.id === serverId)
    return server && kind ? serverGpusOfKind(server, kind) : 0
  }
  // A budget is a whole number from one to the server's GPUs of the kind.
  const badBudget = (gpus: number | undefined, have: number) =>
    !Number.isInteger(gpus) || (gpus ?? 0) < 1 || (gpus ?? 0) > have
  const budgetsValid =
    !kind || input.members.every((m) => !badBudget(m.gpus, gpusOf(m.serverId)))
  const [error, setError] = useState("")
  const busy = create.isPending || update.isPending
  const save = async () => {
    setError("")
    try {
      if (pool)
        await update.mutateAsync({
          path: { cloudId, poolId: pool.id },
          body: input,
        })
      else await create.mutateAsync({ path: { cloudId }, body: input })
      onSaved()
    } catch (err) {
      setError(
        messageOf(
          err,
          "The pool wasn't saved. Refresh it if capacity or membership changed."
        )
      )
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{pool ? "Edit pool" : "Add pool"}</DialogTitle>
          <DialogDescription>
            Reserve explicit usable host budgets. Pool membership does not
            create a private network.
          </DialogDescription>
        </DialogHeader>
        <Field label="Pool name">
          <Input
            aria-label="Pool name"
            value={input.name}
            onChange={(e) => setInput({ ...input, name: e.target.value })}
          />
        </Field>
        <Field label="GPU kind">
          <Select
            value={kind}
            onValueChange={(e) => chooseKind(String(e ?? ""))}
          >
            <SelectTrigger aria-label="GPU kind" className={"w-full min-w-0"}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">None</SelectItem>
              {kinds.map((k) => (
                <SelectItem key={k} value={k}>
                  {gpuTitle(k)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {servers
          .filter(
            (s) =>
              s.status === "ready" ||
              input.members.some((m) => m.serverId === s.id)
          )
          .map((s) => {
            const member = input.members.find((m) => m.serverId === s.id)
            const have = kind ? serverGpusOfKind(s, kind) : 0
            const blocked = !member && !!kind && !have
            return (
              <div key={s.id} className="grid gap-2 rounded-lg bg-muted p-3">
                <FormField
                  orientation="horizontal"
                  className="flex items-center gap-2 text-[13px]"
                >
                  <FieldLabel className="contents">
                    <Checkbox
                      checked={!!member}
                      disabled={blocked}
                      onCheckedChange={(e) =>
                        setInput({
                          ...input,
                          members: e
                            ? [
                                ...input.members,
                                {
                                  serverId: s.id,
                                  cpuMillis: s.health.effectiveCpuMillis,
                                  memoryBytes: s.health.effectiveMemoryBytes,
                                  draining: s.draining,
                                  ...(kind ? { gpus: have } : {}),
                                },
                              ]
                            : input.members.filter((m) => m.serverId !== s.id),
                        })
                      }
                    />
                    {s.name}
                  </FieldLabel>
                </FormField>
                {blocked && (
                  <p className="text-xs text-muted-foreground">
                    No {gpuTitle(kind)} GPUs
                  </p>
                )}
                {member && (
                  <div className="grid gap-2 sm:grid-cols-2">
                    <Field label="CPU budget (millicores)">
                      <Input
                        type="number"
                        min={1}
                        aria-label={`CPU budget for ${s.name}`}
                        value={member.cpuMillis}
                        onChange={(e) =>
                          setInput({
                            ...input,
                            members: input.members.map((m) =>
                              m.serverId === s.id
                                ? { ...m, cpuMillis: Number(e.target.value) }
                                : m
                            ),
                          })
                        }
                      />
                    </Field>
                    <Field label="Memory budget (MiB)">
                      <Input
                        type="number"
                        min={1}
                        aria-label={`Memory budget for ${s.name}`}
                        value={member.memoryBytes / 1048576}
                        onChange={(e) =>
                          setInput({
                            ...input,
                            members: input.members.map((m) =>
                              m.serverId === s.id
                                ? {
                                    ...m,
                                    memoryBytes:
                                      Number(e.target.value) * 1048576,
                                  }
                                : m
                            ),
                          })
                        }
                      />
                    </Field>
                    {kind && (
                      <div className="grid gap-1">
                        <Field label="GPU budget">
                          <Input
                            type="number"
                            min={1}
                            max={have}
                            step={1}
                            aria-label={`GPU budget for ${s.name}`}
                            aria-invalid={badBudget(member.gpus, have)}
                            value={member.gpus ?? ""}
                            onChange={(e) =>
                              setInput({
                                ...input,
                                members: input.members.map((m) =>
                                  m.serverId === s.id
                                    ? {
                                        ...m,
                                        gpus:
                                          e.target.value === ""
                                            ? undefined
                                            : Number(e.target.value),
                                      }
                                    : m
                                ),
                              })
                            }
                          />
                        </Field>
                        {badBudget(member.gpus, have) && (
                          <p className="text-xs text-destructive">
                            {`Between 1 and ${have} GPUs`}
                          </p>
                        )}
                      </div>
                    )}
                    <FormField
                      orientation="horizontal"
                      className="flex items-center gap-2 text-xs"
                    >
                      <FieldLabel className="contents">
                        <Checkbox
                          checked={member.draining}
                          onCheckedChange={(e) =>
                            setInput({
                              ...input,
                              members: input.members.map((m) =>
                                m.serverId === s.id ? { ...m, draining: e } : m
                              ),
                            })
                          }
                        />
                        Drain member
                      </FieldLabel>
                    </FormField>
                  </div>
                )}
              </div>
            )
          })}
        <CloudError message={error} />
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={
              busy ||
              !input.name.trim() ||
              !input.members.length ||
              !budgetsValid
            }
            onClick={() => void save()}
          >
            Save pool budgets
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
function MoveEditor({
  cloudId,
  pool,
  servers,
  onClose,
}: {
  cloudId: string
  pool: Pool
  servers: Server[]
  onClose: () => void
}) {
  const projects = useQuery(api.useProjectsList)
  const preview = useMutation(api.useCloudPlacementsPreviewMove)
  const apply = useMutation(api.useCloudPlacementsAcceptMove)
  const [input, setInput] = useState<MoveInputBody>({
    projectId: "",
    environmentId: "",
    service: "",
    poolId: pool.id,
    destinationServerId: "",
  })
  const [review, setReview] = useState<MoveReview | null>(null)
  const [error, setError] = useState("")
  const [accepted, setAccepted] = useState(false)
  const [environmentName, setEnvironmentName] = useState("")
  const change = (p: Partial<MoveInputBody>) => {
    setInput({ ...input, ...p })
    setReview(null)
  }
  const act = async () => {
    setError("")
    try {
      if (review) {
        await apply.mutateAsync({
          path: { cloudId },
          body: { ...input, review: review.review },
        })
        setAccepted(true)
      } else
        setReview(
          (await preview.mutateAsync({
            path: { cloudId },
            body: input,
          })) as MoveReview
        )
    } catch (err) {
      setError(
        messageOf(
          err,
          "The move wasn't accepted. Review the current placement again."
        )
      )
      setReview(null)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Review service move</DialogTitle>
          <DialogDescription>
            Move one service's current release. Persistent data migration needs
            its own review.
          </DialogDescription>
        </DialogHeader>
        {accepted ? (
          <div className="grid gap-3 text-[13px]">
            <p role="status">
              Move intent staged for {input.service}. It expires 15 minutes
              after acceptance. No deployment has started; the source still
              serves.
            </p>
            <p>
              Create and review a deployment plan for this project and
              environment before the intent expires. The plan consumes the
              reviewed destination intent and checks current capacity.
            </p>
            <Button
              nativeButton={false}
              render={
                <Link
                  to="/deployments/new"
                  search={{
                    project: input.projectId,
                    environment: environmentName,
                  }}
                />
              }
            >
              Create deployment plan
            </Button>
          </div>
        ) : (
          <>
            <SetupReads reads={[{ label: "Projects", query: projects }]}>
              {projects.data?.projects.length === 0 ? (
                <CloudEmpty
                  kind="inline"
                  icon={ProjectsIcon}
                  title="No projects for this move"
                  action={
                    <Button
                      nativeButton={false}
                      render={<Link to="/projects" />}
                    >
                      Open projects
                    </Button>
                  }
                >
                  Create a project and deploy its service before reviewing a
                  move.
                </CloudEmpty>
              ) : (
                <Field label="Move project">
                  <Select
                    value={input.projectId}
                    onValueChange={(e) =>
                      change({ projectId: e ?? "", environmentId: "" })
                    }
                  >
                    <SelectTrigger
                      aria-label="Move project"
                      className={"w-full min-w-0"}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="">Choose project</SelectItem>
                      {projects.data?.projects.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            </SetupReads>
            {input.projectId && (
              <MoveEnvironment
                projectId={input.projectId}
                value={input.environmentId}
                onChange={(environmentId, name) => {
                  setEnvironmentName(name ?? "")
                  change({ environmentId })
                }}
              />
            )}
            <Field label="Service">
              <Input
                aria-label="Move service"
                value={input.service}
                onChange={(e) => change({ service: e.target.value })}
              />
            </Field>
            <Field label="Destination server">
              <Select
                value={input.destinationServerId}
                onValueChange={(e) => change({ destinationServerId: e ?? "" })}
              >
                <SelectTrigger
                  aria-label="Destination server"
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">Choose server</SelectItem>
                  {pool.members
                    .filter((m) => !m.draining)
                    .map((m) => (
                      <SelectItem key={m.serverId} value={m.serverId}>
                        {servers.find((s) => s.id === m.serverId)?.name ??
                          m.serverId}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </Field>
            {review && (
              <p className="text-[13px]">
                Source: {review.sourceServerId} · destination:{" "}
                {input.destinationServerId}. Accept this exact reviewed
                placement.
              </p>
            )}
            <Button
              disabled={
                preview.isPending ||
                apply.isPending ||
                projects.status !== "success" ||
                !environmentName ||
                !input.environmentId ||
                !input.service ||
                !input.destinationServerId
              }
              onClick={() => void act()}
            >
              {review ? "Accept reviewed move" : "Preview move"}
            </Button>
          </>
        )}
        <CloudError message={error} />
      </DialogContent>
    </Dialog>
  )
}
export function MoveEnvironment({
  projectId,
  value,
  onChange,
}: {
  projectId: string
  value: string
  onChange: (v: string, name?: string) => void
}) {
  const environments = useQuery(api.useEnvironmentsList, {
    path: { id: projectId },
  })
  return (
    <Field label="Environment">
      <SetupReads reads={[{ label: "Environments", query: environments }]}>
        {environments.data?.environments.length === 0 ? (
          <CloudEmpty kind="inline" icon={EmptyIcon} title="No environments">
            Add an environment in this project before reviewing a route or move.
          </CloudEmpty>
        ) : (
          <Select
            value={value}
            onValueChange={(event) =>
              onChange(
                event ?? "",
                environments.data?.environments.find(
                  (environment) => environment.id === (event ?? "")
                )?.name
              )
            }
          >
            <SelectTrigger
              aria-label="Route environment"
              className={"w-full min-w-0"}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Choose environment</SelectItem>
              {environments.data?.environments.map((environment) => (
                <SelectItem key={environment.id} value={environment.id}>
                  {environment.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </SetupReads>
    </Field>
  )
}
export function Recovery({
  cloudId,
  initialPlan = "",
  initialService = "",
  onClose,
}: {
  cloudId: string
  initialPlan?: string
  initialService?: string
  onClose: () => void
}) {
  const preview = useMutation(api.useCloudPlacementsPreviewRecovery)
  const recover = useMutation(api.useCloudPlacementsRecover)
  const [planId, setPlan] = useState(initialPlan)
  const [service, setService] = useState(initialService)
  const [review, setReview] = useState<ReservationRecovery | null>(null)
  const [error, setError] = useState("")
  const [done, setDone] = useState(false)
  async function act() {
    setError("")
    try {
      const body = {
        planId,
        service,
        ...(review ? { review: review.review } : {}),
      }
      if (review) {
        const recovered = (await recover.mutateAsync({
          path: { cloudId },
          body,
        })) as ReservationRecovery
        setReview(recovered)
        setDone(true)
        try {
          await Promise.all([
            api
              .useCloudServersUsage({
                path: { cloudId, serverId: recovered.serverId },
              })
              .refetch(),
            api.useCloudServersList({ path: { cloudId } }).refetch(),
            api.useCloudPoolsList({ path: { cloudId } }).refetch(),
          ])
        } catch (refreshError) {
          setError(
            messageOf(
              refreshError,
              "Recovery was recorded, but current usage couldn't be refreshed. Reopen the server and retry its usage read."
            )
          )
        }
      } else
        setReview(
          (await preview.mutateAsync({
            path: { cloudId },
            body,
          })) as ReservationRecovery
        )
    } catch (err) {
      setError(
        messageOf(
          err,
          "Recovery is blocked. Reconcile active attempts and serving bindings before reviewing again."
        )
      )
      setReview(null)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Recover reservation</DialogTitle>
          <DialogDescription>
            Recovery uses the saved review. Active unreconciled attempts and
            serving bindings cannot be abandoned.
          </DialogDescription>
        </DialogHeader>
        <Field label="Plan ID">
          <Input
            aria-label="Recovery plan ID"
            value={planId}
            onChange={(e) => {
              setPlan(e.target.value)
              setReview(null)
              setDone(false)
            }}
          />
        </Field>
        <Field label="Service">
          <Input
            aria-label="Recovery service"
            value={service}
            onChange={(e) => {
              setService(e.target.value)
              setReview(null)
              setDone(false)
            }}
          />
        </Field>
        {review && (
          <div className="grid gap-1 text-[13px]">
            <p>
              Reservation: {review.phase} ·{" "}
              {review.active ? "active" : "inactive"} ·{" "}
              {review.started ? "execution started" : "not started"}
            </p>
            {review.storageRetained && (
              <p>
                Compute cleanup preserved storage. This does not confirm a named
                volume exists.
              </p>
            )}
            {done && <p role="status">Recovery recorded.</p>}
          </div>
        )}
        <CloudError message={error} />
        {!done && (
          <Button
            disabled={
              !planId || !service || preview.isPending || recover.isPending
            }
            onClick={() => void act()}
          >
            {review ? "Apply reviewed recovery" : "Preview recovery"}
          </Button>
        )}
      </DialogContent>
    </Dialog>
  )
}
