// How a Cloud's server pools may grow: the policy, one rule per pool, and
// the servers waiting for someone to approve them. Pools never shrink on
// their own, and nothing grows while this Loomplane keeps automatic
// expansion off.
import type { UseQueryResult } from "@forge-go/client-react"
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CapacityPolicy,
  Connection,
  Expansion,
  ExpansionList,
  Pool,
  PoolExpansionPolicy,
  ResourceVector,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  CloudError,
  CloudSectionHead,
  Field,
  Row,
  Rows,
} from "@/components/cloud-targets/infrastructure-ui"
import { supports } from "@/components/cloud-targets/servers"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { relative } from "@/lib/format"
import { useNow } from "@/lib/use-runs"

import { RunsOn } from "./targets"

const GIB = 2 ** 30
const GB = 1_000_000_000

const emptyVector: ResourceVector = {
  servers: 0,
  cpuMillis: 0,
  memoryBytes: 0,
  storageBytes: 0,
}

/** A number field in the unit people think in, stored as the API's unit. */
function Amount({
  label,
  value,
  scale,
  step = 1,
  onChange,
}: {
  label: string
  value: number
  scale: number
  step?: number
  onChange: (next: number) => void
}) {
  return (
    <Field label={label}>
      <Input
        type="number"
        min={0}
        step={step}
        className="w-28"
        value={value ? Number((value / scale).toFixed(2)) : ""}
        placeholder="0"
        onChange={(e) =>
          onChange(Math.max(0, Math.round(Number(e.target.value) * scale)))
        }
      />
    </Field>
  )
}

export function Capacity({
  cloudId,
  pools,
  connections,
}: {
  cloudId: string
  pools: Pool[]
  connections: Connection[]
}) {
  const view = useQuery(api.useCloudCapacityGetPolicy, { path: { cloudId } })
  const expansions = useQuery(api.useCloudCapacityListExpansions, {
    path: { cloudId },
  })
  if (view.status === "error")
    return (
      <QueryError
        error={view.error}
        fallback="The capacity policy couldn't be loaded."
        retry={view.refetch}
      />
    )
  if (!view.data?.policy) return <Skeleton className="h-64 rounded-xl" />
  const automatic = view.data.automaticExpansion
  return (
    <div className="grid gap-6">
      {!automatic && (
        <section
          aria-label="Automatic expansion is off"
          className="flex gap-3 rounded-xl bg-muted px-4 py-3 ring-1 ring-border"
        >
          <span
            aria-hidden="true"
            className="mt-1.5 size-2 shrink-0 rounded-full bg-muted-foreground/50"
          />
          <span className="grid gap-0.5">
            <span className="text-[13px] font-semibold">
              Automatic expansion isn&apos;t on for this Loomplane yet
            </span>
            <span className="text-[12.5px] leading-relaxed text-muted-foreground">
              You can set the policy now and it&apos;s saved. Until expansion is
              enabled on the server, pools don&apos;t grow on their own: a
              deployment that doesn&apos;t fit waits for a server you add. Pools
              never shrink on their own either; you drain and remove servers
              yourself.
            </span>
          </span>
        </section>
      )}
      <PolicyEditor
        key={view.data.policy.revision}
        cloudId={cloudId}
        policy={view.data.policy}
        pools={pools}
        connections={connections}
        onSaved={() => void view.refetch()}
      />
      <Expansions
        cloudId={cloudId}
        automatic={automatic}
        pools={pools}
        connections={connections}
        query={expansions}
      />
    </div>
  )
}

function PolicyEditor({
  cloudId,
  policy,
  pools,
  connections,
  onSaved,
}: {
  cloudId: string
  policy: CapacityPolicy
  pools: Pool[]
  connections: Connection[]
  onSaved: () => void
}) {
  const save = useMutation(api.useCloudCapacitySavePolicy)
  const [draft, setDraft] = useState<CapacityPolicy>(policy)
  const [editing, setEditing] = useState<Pool | null>(null)
  const [error, setError] = useState("")
  const dirty = JSON.stringify(draft) !== JSON.stringify(policy)
  const limits = draft.limits ?? emptyVector
  const setLimits = (next: Partial<ResourceVector>) =>
    setDraft({ ...draft, limits: { ...limits, ...next } })
  // GPU kinds the pools grow by, each with its own ceiling.
  const kinds = [
    ...new Set([
      ...(draft.gpuLimits ?? []).map((g) => g.kind),
      ...pools.flatMap((p) => (p.acceleratorKind ? [p.acceleratorKind] : [])),
    ]),
  ]
  const gpuLimit = (kind: string) =>
    draft.gpuLimits?.find((g) => g.kind === kind)?.maximum ?? 0
  const submit = async () => {
    setError("")
    try {
      await save.mutateAsync({
        path: { cloudId },
        body: { expectedRevision: policy.revision, policy: draft },
      })
      onSaved()
    } catch (err) {
      setError(
        statusOf(err) === 409
          ? "Someone saved this policy meanwhile. Reload to see theirs."
          : messageOf(
              err,
              "The policy wasn't saved. Turning growth on needs every limit above zero, and a rule for each pool that may grow."
            )
      )
    }
  }
  return (
    <>
      <section aria-label="Growth policy" className="grid gap-3">
        <CloudSectionHead
          title="Growth policy"
          description={`When a deployment needs more room than a pool has, Loomplane may add a server within these limits.${policy.revision ? ` Saved as revision ${policy.revision}.` : " Not saved yet."}`}
        >
          {dirty && (
            <Button
              variant="ghost"
              disabled={save.isPending}
              onClick={() => setDraft(policy)}
            >
              Discard
            </Button>
          )}
          <Button
            disabled={!dirty || save.isPending}
            onClick={() => void submit()}
          >
            Save policy
          </Button>
        </CloudSectionHead>
        <Rows>
          <Row>
            <FormField orientation="horizontal" className="flex-1">
              <FieldLabel className="flex flex-1 items-center justify-between gap-4">
                <span className="grid gap-0.5">
                  <span className="text-[13px] font-medium">
                    Let pools on this Cloud grow
                  </span>
                  <span className="text-xs font-normal text-muted-foreground">
                    Off keeps every pool at the servers it has.
                  </span>
                </span>
                <Switch
                  checked={draft.enabled}
                  onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
                />
              </FieldLabel>
            </FormField>
          </Row>
          <Row>
            <FormField orientation="horizontal" className="flex-1">
              <FieldLabel className="flex flex-1 items-center justify-between gap-4">
                <span className="grid gap-0.5">
                  <span className="text-[13px] font-medium">
                    Ask before adding a server
                  </span>
                  <span className="text-xs font-normal text-muted-foreground">
                    Each new server waits below for someone to approve it. Off
                    adds it as soon as a deployment needs it.
                  </span>
                </span>
                <Switch
                  checked={draft.requireExpansionApproval}
                  onCheckedChange={(requireExpansionApproval) =>
                    setDraft({ ...draft, requireExpansionApproval })
                  }
                />
              </FieldLabel>
            </FormField>
          </Row>
          <Row className="flex-wrap items-end gap-y-3">
            <span className="grid min-w-48 flex-1 gap-0.5">
              <span className="text-[13px] font-medium">
                Most this Cloud may hold
              </span>
              <span className="text-xs text-muted-foreground">
                Across every server it has, including ones it adds.
              </span>
            </span>
            <Amount
              label="Servers"
              value={limits.servers}
              scale={1}
              onChange={(servers) => setLimits({ servers })}
            />
            <Amount
              label="vCPU"
              value={limits.cpuMillis}
              scale={1000}
              step={0.5}
              onChange={(cpuMillis) => setLimits({ cpuMillis })}
            />
            <Amount
              label="Memory, GiB"
              value={limits.memoryBytes}
              scale={GIB}
              onChange={(memoryBytes) => setLimits({ memoryBytes })}
            />
            <Amount
              label="Storage, GB"
              value={limits.storageBytes}
              scale={GB}
              onChange={(storageBytes) => setLimits({ storageBytes })}
            />
            {kinds.map((kind) => (
              <Amount
                key={kind}
                label={`${kind} GPUs`}
                value={gpuLimit(kind)}
                scale={1}
                onChange={(maximum) =>
                  setDraft({
                    ...draft,
                    gpuLimits: [
                      ...(draft.gpuLimits ?? []).filter((g) => g.kind !== kind),
                      { kind, maximum },
                    ],
                  })
                }
              />
            ))}
          </Row>
        </Rows>
        <CloudError message={error} />
      </section>

      <section aria-label="How each pool grows" className="grid gap-3">
        <CloudSectionHead
          title="How each pool grows"
          count={pools.length}
          description="A pool without a rule never grows. A new server joins with its size's capacity, less what you keep back for the system. Changes here are part of the policy, so save it after."
        />
        {pools.length === 0 ? (
          <p className="rounded-xl px-4 py-3 text-[13px] text-muted-foreground ring-1 ring-border">
            This Cloud has no pools yet. Add one on the Servers tab, then give
            it a rule here.
          </p>
        ) : (
          <Rows>
            {pools.map((pool) => {
              const rule = draft.pools.find((r) => r.poolId === pool.id)
              return (
                <Row key={pool.id} className="flex-wrap gap-y-2">
                  <span className="grid min-w-32 flex-[1_1_120px] gap-0.5">
                    <span className="font-mono text-[13px] font-medium">
                      {pool.name}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {pool.members.length}{" "}
                      {pool.members.length === 1 ? "server" : "servers"} now
                    </span>
                  </span>
                  {rule ? (
                    <>
                      <span className="min-w-0 flex-[2_1_240px]">
                        <RunsOn
                          target={{
                            runtime: "managed",
                            provider: "",
                            connectionId: rule.connectionId,
                            region: `${rule.region} · ${rule.architecture}`,
                          }}
                          connections={connections}
                        />
                      </span>
                      <span className="flex min-w-0 flex-[2_1_200px] flex-wrap gap-1">
                        {rule.allowedSizes.map((s) => (
                          <span
                            key={s}
                            className="rounded-md bg-muted px-1.5 font-mono text-[11.5px]"
                          >
                            {s}
                          </span>
                        ))}
                      </span>
                      <span className="w-24 text-[13px]">
                        <span className="font-mono">
                          {rule.minServers} to {rule.maxServers}
                        </span>{" "}
                        <span className="text-xs text-muted-foreground">
                          servers
                        </span>
                      </span>
                      <span className="w-36 text-[12.5px] text-muted-foreground">
                        Keeps back{" "}
                        {Number(
                          (rule.systemReserve.cpuMillis / 1000).toFixed(2)
                        )}{" "}
                        vCPU,{" "}
                        {Number(
                          (rule.systemReserve.memoryBytes / GIB).toFixed(2)
                        )}{" "}
                        GiB
                      </span>
                    </>
                  ) : (
                    <span className="flex-[6_1_400px] text-[12.5px] text-muted-foreground">
                      No rule, so this pool never grows.
                    </span>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={`${rule ? "Edit" : "Add"} the rule for pool ${pool.name}`}
                    onClick={() => setEditing(pool)}
                  >
                    {rule ? "Edit rule" : "Add rule"}
                  </Button>
                </Row>
              )
            })}
          </Rows>
        )}
      </section>
      {editing && (
        <PoolRuleDialog
          cloudId={cloudId}
          pool={editing}
          rule={draft.pools.find((r) => r.poolId === editing.id)}
          connections={connections}
          onClose={() => setEditing(null)}
          onDone={(rule) => {
            const rest = draft.pools.filter((r) => r.poolId !== editing.id)
            setDraft({ ...draft, pools: rule ? [...rest, rule] : rest })
            setEditing(null)
          }}
        />
      )}
    </>
  )
}

function PoolRuleDialog({
  cloudId,
  pool,
  rule,
  connections,
  onClose,
  onDone,
}: {
  cloudId: string
  pool: Pool
  rule?: PoolExpansionPolicy
  connections: Connection[]
  onClose: () => void
  /** The edited rule, or null to remove it. */
  onDone: (rule: PoolExpansionPolicy | null) => void
}) {
  const providers = useQuery(api.useProvidersList)
  const accounts = connections.filter(
    (c) =>
      c.status === "ready" &&
      supports(providers.data?.providers ?? [], c, "server:provision")
  )
  const [value, setValue] = useState<PoolExpansionPolicy>(
    rule ?? {
      poolId: pool.id,
      acceleratorKind: pool.acceleratorKind,
      connectionId: "",
      region: "",
      architecture: "amd64",
      image: "",
      bootstrapSize: "",
      allowedSizes: [],
      minServers: 0,
      maxServers: 1,
      systemReserve: {
        ...emptyVector,
        cpuMillis: 250,
        memoryBytes: 512 * 2 ** 20,
      },
    }
  )
  const set = (next: Partial<PoolExpansionPolicy>) =>
    setValue({ ...value, ...next })
  const complete =
    !!value.connectionId &&
    !!value.region &&
    !!value.image &&
    value.allowedSizes.length > 0 &&
    value.minServers <= value.maxServers
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>How pool {pool.name} grows</DialogTitle>
          <DialogDescription>
            New servers for this pool are created on this account, in this
            region, from one of the sizes you tick.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <Field label="Account">
            <Select
              value={value.connectionId}
              onValueChange={(v) =>
                set({
                  connectionId: v ?? "",
                  region: "",
                  image: "",
                  allowedSizes: [],
                })
              }
            >
              <SelectTrigger aria-label="Account" className="w-full min-w-0">
                <SelectValue placeholder="An account that can create servers" />
              </SelectTrigger>
              <SelectContent>
                {accounts.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {value.connectionId && (
            <CatalogFields
              key={value.connectionId}
              cloudId={cloudId}
              pool={pool}
              value={value}
              set={set}
            />
          )}
          <div className="flex flex-wrap gap-3">
            <Amount
              label="Fewest servers"
              value={value.minServers}
              scale={1}
              onChange={(minServers) => set({ minServers })}
            />
            <Amount
              label="Most servers"
              value={value.maxServers}
              scale={1}
              onChange={(maxServers) => set({ maxServers })}
            />
            <Amount
              label="Keep back, vCPU"
              value={value.systemReserve.cpuMillis}
              scale={1000}
              step={0.25}
              onChange={(cpuMillis) =>
                set({ systemReserve: { ...value.systemReserve, cpuMillis } })
              }
            />
            <Amount
              label="Keep back, GiB"
              value={value.systemReserve.memoryBytes}
              scale={GIB}
              step={0.25}
              onChange={(memoryBytes) =>
                set({ systemReserve: { ...value.systemReserve, memoryBytes } })
              }
            />
          </div>
        </div>
        <DialogFooter className="flex-wrap">
          {rule && (
            <Button
              variant="outline"
              className="mr-auto text-destructive"
              onClick={() => onDone(null)}
            >
              Remove rule
            </Button>
          )}
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!complete} onClick={() => onDone(value)}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** CatalogFields offers the regions, images and sizes one account sells. */
function CatalogFields({
  cloudId,
  pool,
  value,
  set,
}: {
  cloudId: string
  pool: Pool
  value: PoolExpansionPolicy
  set: (next: Partial<PoolExpansionPolicy>) => void
}) {
  const options = useQuery(api.useCloudServersOptions, {
    path: { cloudId, connectionId: value.connectionId },
  })
  const catalog = options.data
  const sizes = (catalog?.sizes ?? []).filter(
    (s) =>
      (!value.region || s.regions.includes(value.region)) &&
      (!s.architectures?.length ||
        s.architectures.includes(value.architecture)) &&
      (s.accelerator?.kind ?? "") === (pool.acceleratorKind ?? "")
  )
  const images = (catalog?.images ?? []).filter(
    (i) =>
      (!value.region || i.regions.includes(value.region)) &&
      (!i.architecture || i.architecture === value.architecture)
  )
  return (
    <>
      {options.status === "error" && (
        <QueryError
          error={options.error}
          fallback="This account's regions and sizes couldn't be loaded."
          retry={options.refetch}
        />
      )}
      {catalog && (
        <>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Region">
              <Select
                value={value.region}
                onValueChange={(v) =>
                  set({ region: v ?? "", image: "", allowedSizes: [] })
                }
              >
                <SelectTrigger aria-label="Region" className="w-full min-w-0">
                  <SelectValue placeholder="Choose a region" />
                </SelectTrigger>
                <SelectContent>
                  {catalog.regions.map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.name || r.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Architecture">
              <Select
                value={value.architecture}
                onValueChange={(v) =>
                  set({
                    architecture: v ?? "amd64",
                    image: "",
                    allowedSizes: [],
                  })
                }
              >
                <SelectTrigger
                  aria-label="Architecture"
                  className="w-full min-w-0"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="amd64">x86 (amd64)</SelectItem>
                  <SelectItem value="arm64">Arm (arm64)</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field label="Image">
            <Select
              value={value.image}
              onValueChange={(v) => set({ image: v ?? "" })}
            >
              <SelectTrigger aria-label="Image" className="w-full min-w-0">
                <SelectValue placeholder="Choose an image" />
              </SelectTrigger>
              <SelectContent>
                {images.map((i) => (
                  <SelectItem key={i.id} value={i.id}>
                    {i.name || i.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <fieldset className="grid gap-1.5">
            <legend className="mb-1.5 text-xs">Sizes it may add</legend>
            {sizes.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground">
                {value.region
                  ? "No size here fits this pool's region and architecture."
                  : "Choose a region first."}
              </p>
            ) : (
              sizes.map((s) => {
                const on = value.allowedSizes.includes(s.id)
                return (
                  <FormField
                    key={s.id}
                    orientation="horizontal"
                    className="flex items-center gap-3 rounded-lg px-3 py-2 ring-1 ring-border"
                  >
                    <FieldLabel className="contents">
                      <Checkbox
                        checked={on}
                        onCheckedChange={() =>
                          set({
                            allowedSizes: on
                              ? value.allowedSizes.filter((x) => x !== s.id)
                              : [...value.allowedSizes, s.id],
                          })
                        }
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">
                        {s.id}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {s.cpus} vCPU ·{" "}
                        {Number((s.memoryMib / 1024).toFixed(1))} GiB
                        {s.hourlyPriceKnown !== false && s.monthlyUsd
                          ? ` · $${Math.round(s.monthlyUsd)} a month`
                          : ""}
                      </span>
                    </FieldLabel>
                  </FormField>
                )
              })
            )}
          </fieldset>
        </>
      )}
    </>
  )
}

const blockWords: Record<string, string> = {
  expansion_approval: "Waiting for approval",
  review_required: "Needs review",
  limit: "Over a limit",
  provisioning: "Being created",
}

function Expansions({
  cloudId,
  automatic,
  pools,
  connections,
  query,
}: {
  cloudId: string
  automatic: boolean
  pools: Pool[]
  connections: Connection[]
  query: UseQueryResult<ExpansionList>
}) {
  const approve = useMutation(api.useCloudCapacityApproveExpansion)
  const now = useNow()
  const [error, setError] = useState("")
  const waiting = (query.data?.expansions ?? []).filter(
    (e): e is Expansion =>
      !!e && (e.status === "awaiting_approval" || e.status === "blocked")
  )
  const act = async (e: Expansion) => {
    setError("")
    try {
      await approve.mutateAsync({
        path: { cloudId, expansionId: e.id },
        body: { expectedRevision: e.revision },
      })
      await query.refetch()
    } catch (err) {
      setError(
        messageOf(err, "The server wasn't approved. Refresh and try again.")
      )
    }
  }
  return (
    <section aria-label="Servers waiting for approval" className="grid gap-3">
      <CloudSectionHead
        title="Servers waiting for approval"
        count={query.data ? waiting.length : undefined}
        description="Each one is a server a deployment needs. Approving creates it on the account shown, at the price shown."
      />
      {query.status === "error" ? (
        <QueryError
          error={query.error}
          fallback="Waiting servers couldn't be loaded."
          retry={query.refetch}
        />
      ) : !query.data ? (
        <Skeleton className="h-16 rounded-xl" />
      ) : waiting.length === 0 ? (
        <p className="rounded-xl border border-dashed px-4 py-3 text-[12.5px] leading-relaxed text-muted-foreground">
          {automatic
            ? "Nothing is waiting."
            : "Nothing is waiting. While automatic expansion is off, Loomplane doesn't propose servers, so nothing will appear here until it's turned on."}
        </p>
      ) : (
        <Rows>
          {waiting.map((e) => {
            const pool = pools.find((p) => p.id === e.poolId)
            const account = connections.find((c) => c.id === e.connectionId)
            const reason = e.block?.reason ?? ""
            return (
              <Row key={e.id} className="flex-wrap gap-y-2">
                <span className="grid min-w-0 flex-[1_1_360px] gap-0.5">
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="text-[13px] font-semibold">
                      Add a {e.size} server to pool{" "}
                      {pool?.name ?? "that was removed"}
                    </span>
                    <StatusPill
                      tone={
                        e.status === "awaiting_approval" ? "waiting" : "failed"
                      }
                    >
                      {e.status === "awaiting_approval"
                        ? "Waiting for you"
                        : (blockWords[reason] ?? "Blocked")}
                    </StatusPill>
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {[
                      account?.name ?? "A removed account",
                      e.region,
                      e.hourlyPriceKnown && e.estimatedHourlyUsd
                        ? `about $${(e.estimatedHourlyUsd * 730).toFixed(0)} a month`
                        : "price unknown",
                      `asked ${relative(Date.parse(e.createdAt), now)}`,
                    ].join(" · ")}
                  </span>
                </span>
                {automatic && e.status === "awaiting_approval" && (
                  <Button
                    size="sm"
                    disabled={approve.isPending}
                    onClick={() => void act(e)}
                  >
                    Approve
                  </Button>
                )}
              </Row>
            )
          })}
        </Rows>
      )}
      <CloudError message={error} />
    </section>
  )
}
