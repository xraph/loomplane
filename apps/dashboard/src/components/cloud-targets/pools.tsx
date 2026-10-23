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
import { messageOf } from "@/client/errors"
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
import { selectClass } from "./workspace"
import {
  CloudEmpty,
  CloudError,
  Field,
  Rows,
  rowClass,
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
  return (
    <section className="grid gap-3" aria-label="Server pools">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">Pools and reservations</h3>
        <div className="flex flex-wrap gap-2">
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
        </div>
      </div>
      {pools.status === "error" ? (
        <QueryError
          error={pools.error}
          fallback="Pools couldn't be loaded."
          retry={pools.refetch}
        />
      ) : !pools.data ? (
        <p role="status">Loading pools...</p>
      ) : !list.length ? (
        <CloudEmpty title="No server pools">
          Add ready servers to a pool and choose their usable CPU and memory
          budgets.
        </CloudEmpty>
      ) : (
        <Rows>
          {list.map((p) => (
            <div className={rowClass} key={p.id}>
              <div>
                <p className="font-medium">{p.name}</p>
                <p className="text-xs text-muted-foreground">
                  {p.members.length} members ·{" "}
                  {p.members.filter((m) => m.draining).length} draining ·
                  revision {p.revision}
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
            </div>
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
  })
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
        {servers
          .filter(
            (s) =>
              s.status === "ready" ||
              input.members.some((m) => m.serverId === s.id)
          )
          .map((s) => {
            const member = input.members.find((m) => m.serverId === s.id)
            return (
              <div key={s.id} className="grid gap-2 rounded-lg bg-muted p-3">
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={!!member}
                    onChange={(e) =>
                      setInput({
                        ...input,
                        members: e.target.checked
                          ? [
                              ...input.members,
                              {
                                serverId: s.id,
                                cpuMillis: s.health.effectiveCpuMillis,
                                memoryBytes: s.health.effectiveMemoryBytes,
                                draining: s.draining,
                              },
                            ]
                          : input.members.filter((m) => m.serverId !== s.id),
                      })
                    }
                  />
                  {s.name}
                </label>
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
                    <label className="flex items-center gap-2 text-xs">
                      <input
                        type="checkbox"
                        checked={member.draining}
                        onChange={(e) =>
                          setInput({
                            ...input,
                            members: input.members.map((m) =>
                              m.serverId === s.id
                                ? { ...m, draining: e.target.checked }
                                : m
                            ),
                          })
                        }
                      />
                      Drain member
                    </label>
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
            disabled={busy || !input.name.trim() || !input.members.length}
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
          <p role="status">
            Move accepted. Review applied deployment and routes in the project's
            Activity before treating the destination as serving.
          </p>
        ) : (
          <>
            <Field label="Move project">
              <select
                aria-label="Move project"
                className={selectClass}
                value={input.projectId}
                onChange={(e) =>
                  change({ projectId: e.target.value, environmentId: "" })
                }
              >
                <option value="">Choose project</option>
                {projects.data?.projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </Field>
            {input.projectId && (
              <MoveEnvironment
                projectId={input.projectId}
                value={input.environmentId}
                onChange={(environmentId) => change({ environmentId })}
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
              <select
                aria-label="Destination server"
                className={selectClass}
                value={input.destinationServerId}
                onChange={(e) =>
                  change({ destinationServerId: e.target.value })
                }
              >
                <option value="">Choose server</option>
                {pool.members
                  .filter((m) => !m.draining)
                  .map((m) => (
                    <option key={m.serverId} value={m.serverId}>
                      {servers.find((s) => s.id === m.serverId)?.name ??
                        m.serverId}
                    </option>
                  ))}
              </select>
            </Field>
            {review && (
              <p className="text-sm">
                Source: {review.sourceServerId} · destination:{" "}
                {input.destinationServerId}. Accept this exact reviewed
                placement.
              </p>
            )}
            <Button
              disabled={
                preview.isPending ||
                apply.isPending ||
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
  onChange: (v: string) => void
}) {
  const environments = useQuery(api.useEnvironmentsList, {
    path: { id: projectId },
  })
  return (
    <Field label="Environment">
      {environments.status === "error" ? (
        <QueryError
          error={environments.error}
          fallback="Environments couldn't be loaded."
          retry={environments.refetch}
        />
      ) : (
        <select
          aria-label="Route environment"
          className={selectClass}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">Choose environment</option>
          {environments.data?.environments.map((e) => (
            <option key={e.id} value={e.id}>
              {e.name}
            </option>
          ))}
        </select>
      )}
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
        setReview(
          (await recover.mutateAsync({
            path: { cloudId },
            body,
          })) as ReservationRecovery
        )
        setDone(true)
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
          <div className="grid gap-1 text-sm">
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
