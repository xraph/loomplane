// How a Cloud's server pools may grow: what the Cloud counts now, the
// policy, one rule per pool, and the servers waiting for someone to approve
// them. Pools never shrink on their own, and nothing grows while this
// Loomplane keeps automatic expansion off.
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
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  CloudError,
  CloudSectionHead,
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
import { blockText, conflictText, staleText } from "@/lib/capacity"
import {
  blankRule,
  ceilings,
  check,
  checkRule,
  cloudValues,
  draftOf,
  frontendTargets,
  read,
  tighterFrom,
  type Draft,
  type FrontendDraft,
  type RuleDraft,
} from "@/lib/capacity-policy"
import { relative } from "@/lib/format"
import { acceleratorLabel, gpuTitle, nvidiaKinds, priceKnown } from "@/lib/gpu"
import { useNow } from "@/lib/use-runs"

import { CapacityUsage } from "./capacity-usage"
import { RuleFields, TextAmount } from "./capacity-rule"
import { NewScalingPool } from "./scaling-pool"
import { RunsOn } from "./targets"

const GIB = 2 ** 30

// The 422 a save gets when the server has no sentence of its own for it.
const invalidText =
  "The policy wasn't saved. Check that each pool may grow to no more servers than the Cloud allows, and that its account still offers the region, image and sizes you chose."

export function Capacity({
  cloudId,
  pools,
  connections,
  onPoolsChanged,
}: {
  cloudId: string
  pools: Pool[]
  connections: Connection[]
  /** A pool was created here, so the page reads its pools again. */
  onPoolsChanged?: () => void
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
    <div className="grid min-w-0 gap-6">
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
      <CapacityUsage cloudId={cloudId} />
      <PolicyEditor
        cloudId={cloudId}
        policy={view.data.policy}
        automatic={automatic}
        pools={pools}
        connections={connections}
        reload={view.refetch}
        onPoolsChanged={onPoolsChanged}
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

/** The accounts Loomplane can create servers with. */
function useEligible(connections: Connection[]) {
  const providers = useQuery(api.useProvidersList)
  return connections.filter(
    (c) =>
      c.status === "ready" &&
      supports(providers.data?.providers ?? [], c, "server:provision")
  )
}

function PolicyEditor({
  cloudId,
  policy,
  automatic,
  pools,
  connections,
  reload,
  onPoolsChanged,
}: {
  cloudId: string
  policy: CapacityPolicy
  automatic: boolean
  pools: Pool[]
  connections: Connection[]
  reload: () => Promise<unknown>
  onPoolsChanged?: () => void
}) {
  const save = useMutation(api.useCloudCapacitySavePolicy)
  const providers = useQuery(api.useProvidersList)
  const targetList = useQuery(api.useOrganizationCloudTargetsList, {
    path: { cloudId },
  })
  const accounts = useEligible(connections)
  // The policy the draft was opened from. A refetch moves `policy`, never
  // these while someone has changes, so nothing typed is lost; Reload
  // starts again from the newest.
  const [base, setBase] = useState(policy)
  const [draft, setDraft] = useState<Draft>(() => draftOf(policy))
  const [stale, setStale] = useState(false)
  const [error, setError] = useState("")
  const [editing, setEditing] = useState<Pool | null>(null)
  const [scaling, setScaling] = useState(false)
  // Kinds someone added a GPU ceiling for, kept while the field is empty.
  const [added, setAdded] = useState<string[]>([])
  const dirty = JSON.stringify(draft) !== JSON.stringify(draftOf(base))
  if (policy.revision !== base.revision && !dirty && !stale) {
    // Nothing is being edited, so a newer saved policy simply shows.
    setBase(policy)
    setDraft(draftOf(policy))
  }
  // Frontend limits are rebuilt from the live targets on every save, so the
  // targets must be read first or a save would drop the saved limits.
  const targetsRead = !!targetList.data && !!providers.data
  const targets = frontendTargets(
    (targetList.data?.targets ?? []).filter((t): t is Target => !!t),
    providers.data?.providers ?? []
  )
  const cloud = cloudValues(draft, base)
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  const setCeiling = (key: (typeof ceilings)[number]["key"], v: string) =>
    setDraft((d) => ({ ...d, ceilings: { ...d.ceilings, [key]: v } }))

  // GPU kinds the pools grow by, and any ceiling saved or added, each with
  // its own ceiling.
  const kinds = [
    ...new Set([
      ...Object.keys(draft.gpus),
      ...pools.flatMap((p) => (p.acceleratorKind ? [p.acceleratorKind] : [])),
      ...added,
    ]),
  ]
  const zeroTyped = ceilings.some(
    (c) => read(draft.ceilings[c.key], c.whole) === 0
  )
  const stuckKinds = kinds.filter(
    (k) =>
      pools.some((p) => p.acceleratorKind === k) &&
      !((read(draft.gpus[k] ?? "", true) ?? 0) > 0)
  )

  const submit = async () => {
    setError("")
    setStale(false)
    const checked = check(draft, base, pools, targets)
    if (!checked.policy) {
      setError(checked.problems.join(" "))
      return
    }
    try {
      const saved = await save.mutateAsync({
        path: { cloudId },
        body: { expectedRevision: base.revision, policy: checked.policy },
      })
      if (saved.policy) {
        setBase(saved.policy)
        setDraft(draftOf(saved.policy))
      }
      void reload().catch(() => undefined)
    } catch (err) {
      const raw = err instanceof Error ? err.message : ""
      if (statusOf(err) === 409) {
        // Kept until the next Save, Reload or Discard, while the newer
        // policy loads underneath.
        setStale(true)
        void reload().catch(() => undefined)
      } else if (
        statusOf(err) === 422 &&
        /^invalid infrastructure request/i.test(raw)
      ) {
        setError(invalidText)
      } else {
        setError(
          messageOf(
            err,
            "The policy wasn't saved. Turning growth on needs every limit above zero, and a rule for each pool that may grow."
          )
        )
      }
    }
  }
  const restart = () => {
    setBase(policy)
    setDraft(draftOf(policy))
    setAdded([])
    setStale(false)
    setError("")
  }

  return (
    <>
      <section aria-label="Growth policy" className="grid min-w-0 gap-3">
        <CloudSectionHead
          title="Growth policy"
          description={`When a deployment needs more room than a pool has, Loomplane may add a server within these limits.${base.revision ? ` Saved as revision ${base.revision}.` : " Not saved yet."}`}
        >
          {dirty && (
            <Button variant="ghost" disabled={save.isPending} onClick={restart}>
              Discard
            </Button>
          )}
          <Button
            disabled={!dirty || save.isPending || !targetsRead}
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
                    {automatic
                      ? "Off keeps every pool at the servers it has."
                      : "Off keeps every pool at the servers it has. On is saved, but nothing grows until automatic expansion is on for this Loomplane."}
                  </span>
                </span>
                <Switch
                  aria-label="Let pools on this Cloud grow"
                  checked={draft.enabled}
                  onCheckedChange={(enabled) => set({ enabled })}
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
                  aria-label="Ask before adding a server"
                  checked={draft.approval}
                  onCheckedChange={(approval) => set({ approval })}
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
                Across every server it has, including ones it adds. Once saved,
                these apply to servers you create, adopt or resize too, even
                while pools don&apos;t grow.
              </span>
            </span>
            {ceilings.map((c) => (
              <TextAmount
                key={c.key}
                label={c.label}
                value={draft.ceilings[c.key]}
                whole={c.whole}
                onChange={(v) => setCeiling(c.key, v)}
              />
            ))}
            {kinds.map((kind) => (
              <TextAmount
                key={kind}
                label={`${gpuTitle(kind)} GPUs`}
                value={draft.gpus[kind] ?? ""}
                whole
                onChange={(maximum) => {
                  const gpus = { ...draft.gpus }
                  // An empty GPU field is no ceiling at all, never 0.
                  if (maximum.trim() === "") delete gpus[kind]
                  else gpus[kind] = maximum
                  set({ gpus })
                }}
              />
            ))}
            <AddGpuCeiling
              used={kinds}
              onAdd={(kind) => setAdded((a) => [...a, kind])}
            />
          </Row>
          {(zeroTyped || stuckKinds.length > 0) && (
            <Row className="grid gap-1 text-xs text-muted-foreground">
              {zeroTyped && (
                <span>
                  A limit of 0 is a real limit: nothing more of it fits on this
                  Cloud, including servers you add yourself.
                </span>
              )}
              {stuckKinds.map((k) => (
                <span key={k}>
                  {`Pools of ${gpuTitle(k)} can't grow until its GPU ceiling is above zero.`}
                </span>
              ))}
            </Row>
          )}
        </Rows>
        {stale && (
          <div className="flex flex-wrap items-center gap-3">
            <div className="min-w-0 flex-[1_1_240px]">
              <CloudError message={staleText} />
            </div>
            <Button variant="outline" size="sm" onClick={restart}>
              Reload
            </Button>
          </div>
        )}
        <CloudError message={error} />
      </section>

      <section aria-label="How each pool grows" className="grid min-w-0 gap-3">
        <CloudSectionHead
          title="How each pool grows"
          count={pools.length}
          description="A pool without a rule never grows. A new server joins with its size's capacity, less what you keep back for the system. Changes here are part of the policy, so save it after."
        >
          <Button variant="outline" onClick={() => setScaling(true)}>
            New scaling pool
          </Button>
        </CloudSectionHead>
        {pools.length === 0 ? (
          <p className="rounded-xl px-4 py-3 text-[13px] text-muted-foreground ring-1 ring-border">
            This Cloud has no pools yet. Start one with no servers using New
            scaling pool, or add one from servers you have on the Servers tab
            and give it a rule here.
          </p>
        ) : (
          <Rows>
            {pools.map((pool) => (
              <PoolRow
                key={pool.id}
                pool={pool}
                rule={draft.rules[pool.id]}
                connections={connections}
                onEdit={() => setEditing(pool)}
              />
            ))}
          </Rows>
        )}
      </section>

      {targets.length > 0 && (
        <section aria-label="Frontend limits" className="grid min-w-0 gap-3">
          <CloudSectionHead
            title="Frontend limits"
            count={targets.length}
            description="How many hosted frontends each target may run. Services already there can still redeploy at the limit. These are part of the policy, so save it after."
          />
          <Rows>
            {targets.map((t) => (
              <FrontendRow
                key={t.id}
                name={t.displayName || t.name || t.id}
                value={
                  draft.frontends[t.id] ?? { unlimited: true, maximum: "" }
                }
                onChange={(f) =>
                  set({ frontends: { ...draft.frontends, [t.id]: f } })
                }
              />
            ))}
          </Rows>
        </section>
      )}

      {editing && (
        <PoolRuleDialog
          cloudId={cloudId}
          pool={editing}
          rule={draft.rules[editing.id]}
          accounts={accounts}
          enabled={draft.enabled}
          cloud={cloud}
          start={() =>
            tighterFrom(
              draft.ceilings,
              draft.gpus[editing.acceleratorKind ?? ""] ?? ""
            )
          }
          onClose={() => setEditing(null)}
          onDone={(rule) => {
            const rules = { ...draft.rules }
            if (rule) rules[editing.id] = rule
            else delete rules[editing.id]
            set({ rules })
            setEditing(null)
          }}
        />
      )}
      {scaling && (
        <NewScalingPool
          cloudId={cloudId}
          policy={policy}
          automatic={automatic}
          unsaved={dirty}
          pools={pools}
          accounts={accounts}
          reload={reload}
          onClose={() => setScaling(false)}
          onCreated={() => {
            setScaling(false)
            onPoolsChanged?.()
          }}
        />
      )}
    </>
  )
}

/** AddGpuCeiling offers the GPU kinds that have no ceiling field yet. */
function AddGpuCeiling({
  used,
  onAdd,
}: {
  used: string[]
  onAdd: (kind: string) => void
}) {
  const left = nvidiaKinds.filter((k) => !used.includes(k))
  if (!left.length) return null
  return (
    <Select value="" onValueChange={(v) => v && onAdd(String(v))}>
      <SelectTrigger aria-label="Add a GPU ceiling" className="w-44">
        <SelectValue placeholder="Add a GPU ceiling" />
      </SelectTrigger>
      <SelectContent>
        {left.map((k) => (
          <SelectItem key={k} value={k}>
            {gpuTitle(k)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function PoolRow({
  pool,
  rule,
  connections,
  onEdit,
}: {
  pool: Pool
  rule?: RuleDraft
  connections: Connection[]
  onEdit: () => void
}) {
  const active = rule?.connectionId ? rule : undefined
  return (
    <Row className="flex-wrap gap-y-2">
      <span className="grid min-w-32 flex-[1_1_120px] gap-0.5">
        <span className="font-mono text-[13px] font-medium break-all">
          {pool.name}
        </span>
        <span className="text-xs text-muted-foreground">
          {pool.members.length}{" "}
          {pool.members.length === 1 ? "server" : "servers"} now
          {pool.acceleratorKind ? ` · ${gpuTitle(pool.acceleratorKind)}` : ""}
        </span>
      </span>
      {active ? (
        <>
          <span className="min-w-0 flex-[2_1_240px]">
            <RunsOn
              target={{
                runtime: "managed",
                provider: "",
                connectionId: active.connectionId,
                region: `${active.region} · ${active.architecture}`,
              }}
              connections={connections}
            />
          </span>
          <span className="flex min-w-0 flex-[2_1_200px] flex-wrap gap-1">
            {active.allowedSizes.map((s) => (
              <span
                key={s}
                className="rounded-md bg-muted px-1.5 font-mono text-[11.5px] break-all"
              >
                {s}
              </span>
            ))}
          </span>
          <span className="w-24 text-[13px]">
            <span className="font-mono">
              {active.minServers} to {active.maxServers}
            </span>{" "}
            <span className="text-xs text-muted-foreground">servers</span>
          </span>
          <span className="w-36 text-[12.5px] text-muted-foreground">
            Keeps back{" "}
            {Number((active.systemReserve.cpuMillis / 1000).toFixed(2))} vCPU,{" "}
            {Number((active.systemReserve.memoryBytes / GIB).toFixed(2))} GiB
            {active.limits ? ", with its own limits" : ""}
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
        aria-label={`${active ? "Edit" : "Add"} the rule for pool ${pool.name}`}
        onClick={onEdit}
      >
        {active ? "Edit rule" : "Add rule"}
      </Button>
    </Row>
  )
}

function PoolRuleDialog({
  cloudId,
  pool,
  rule,
  accounts,
  enabled,
  cloud,
  start,
  onClose,
  onDone,
}: {
  cloudId: string
  pool: Pool
  rule?: RuleDraft
  accounts: Connection[]
  enabled: boolean
  cloud: ReturnType<typeof cloudValues>
  /** Where the pool's own limits start: the Cloud's, as typed now. */
  start: () => NonNullable<RuleDraft["limits"]>
  onClose: () => void
  /** The edited rule, or null to remove it. */
  onDone: (rule: RuleDraft | null) => void
}) {
  const [value, setValue] = useState<RuleDraft>(rule ?? blankRule)
  const kind = pool.acceleratorKind ?? ""
  const set = (next: Partial<RuleDraft>) => setValue((v) => ({ ...v, ...next }))
  const checked = checkRule(value, pool, cloud, enabled)
  const chosen =
    !!value.connectionId &&
    !!value.region &&
    !!value.image &&
    value.allowedSizes.length > 0
  const problems = [
    ...(value.connectionId && !chosen
      ? ["Choose a region, an image and at least one size."]
      : []),
    ...checked.problems,
  ]
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="break-all">
            How pool {pool.name} grows
          </DialogTitle>
          <DialogDescription>
            New servers for this pool are created on this account, in this
            region, from one of the sizes you tick.
          </DialogDescription>
        </DialogHeader>
        <p className="text-xs text-muted-foreground">
          {kind
            ? `GPU kind: ${gpuTitle(kind)}, so only its sizes and images with NVIDIA drivers are offered. Change the kind in the pool editor on the Servers tab.`
            : "A CPU pool, so only sizes and images without GPUs are offered. Give it a GPU kind in the pool editor on the Servers tab."}
        </p>
        <RuleFields
          cloudId={cloudId}
          kind={kind}
          value={value}
          accounts={accounts}
          set={set}
          onTighter={(on) => set({ limits: on ? start() : null })}
        />
        {problems.length > 0 && (
          <ul
            aria-live="polite"
            className="grid gap-1 text-xs text-muted-foreground"
          >
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
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
          <Button
            disabled={!chosen || !checked.template}
            onClick={() => onDone(value)}
          >
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function FrontendRow({
  name,
  value,
  onChange,
}: {
  name: string
  value: FrontendDraft
  onChange: (f: FrontendDraft) => void
}) {
  const n = read(value.maximum, true)
  return (
    <Row className="flex-wrap items-start">
      <span className="min-w-0 flex-[1_1_160px] pt-1 text-[13px] break-all">
        {name}
      </span>
      <div className="flex flex-wrap items-start gap-x-4 gap-y-2">
        <label className="flex items-center gap-2 pt-1 text-[13px]">
          <Checkbox
            aria-label={`Unlimited frontends on ${name}`}
            checked={value.unlimited}
            onCheckedChange={(on) => onChange({ ...value, unlimited: on })}
          />
          Unlimited
        </label>
        {!value.unlimited && (
          <div className="grid w-28 gap-1">
            <Input
              type="number"
              min={0}
              step={1}
              aria-label={`Frontend limit for ${name}`}
              value={value.maximum}
              aria-invalid={Number.isNaN(n) || undefined}
              onChange={(e) => onChange({ ...value, maximum: e.target.value })}
            />
            {n === 0 && (
              <span className="text-xs text-muted-foreground">
                No new frontends
              </span>
            )}
          </div>
        )}
      </div>
    </Row>
  )
}

const blockWords: Record<string, string> = {
  expansion_approval: "Waiting for approval",
  review_required: "Needs review",
  hard_limit: "Over a limit",
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
  const waiting = (query.data?.expansions ?? []).filter(
    (e): e is Expansion =>
      !!e && (e.status === "awaiting_approval" || e.status === "blocked")
  )
  return (
    <section
      aria-label="Servers waiting for approval"
      className="grid min-w-0 gap-3"
    >
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
          {waiting.map((e) => (
            // Keyed by ID alone: after a 409 the reload sentence stays while
            // the row shows the new revision, until the next approval.
            <ExpansionRow
              key={e.id}
              cloudId={cloudId}
              expansion={e}
              automatic={automatic}
              pool={pools.find((p) => p.id === e.poolId)?.name}
              account={connections.find((c) => c.id === e.connectionId)?.name}
              reload={query.refetch}
            />
          ))}
        </Rows>
      )}
    </section>
  )
}

// A price reads per month, and an unknown one says so, never $0.
function expansionPrice(e: Expansion): string {
  return priceKnown(e.estimatedHourlyUsd, e.hourlyPriceKnown)
    ? `about $${Math.round(e.estimatedHourlyUsd * 730).toLocaleString("en-US")} a month`
    : "price unknown"
}

function expansionGpus(e: Expansion): string {
  if (e.accelerator?.count) return acceleratorLabel(e.accelerator)
  const r = e.resources
  if (r?.gpus && r.gpuKind)
    return acceleratorLabel({ count: r.gpus, kind: r.gpuKind })
  return ""
}

function ExpansionRow({
  cloudId,
  expansion: e,
  automatic,
  pool,
  account,
  reload,
}: {
  cloudId: string
  expansion: Expansion
  automatic: boolean
  pool?: string
  account?: string
  reload: () => Promise<unknown>
}) {
  const approve = useMutation(api.useCloudCapacityApproveExpansion)
  const now = useNow()
  const [error, setError] = useState("")
  const reason = e.block?.reason ?? ""
  // Over a hard limit, nothing is approved until the limit goes up. Saving
  // a higher limit offers the server for approval again.
  const overLimit = reason === "hard_limit"
  const blockSentence = e.block ? blockText(e.block) : ""
  // Once the reload shows the saved block, the same sentence isn't repeated.
  const shownError = error === blockSentence ? "" : error
  const gpus = expansionGpus(e)
  const act = async () => {
    setError("")
    try {
      await approve.mutateAsync({
        path: { cloudId, expansionId: e.id },
        body: { expectedRevision: e.revision },
      })
      await reload()
    } catch (err) {
      const status = statusOf(err)
      // A 409 is a moved expansion or work already running; a 422 carries
      // the block's sentence. Either way the row reads again.
      setError(
        status === 409
          ? conflictText(err)
          : messageOf(err, "The server wasn't approved. Refresh and try again.")
      )
      if (status === 409 || status === 422) void reload().catch(() => undefined)
    }
  }
  return (
    <Row className="flex-wrap gap-y-2">
      <span className="grid min-w-0 flex-[1_1_300px] gap-0.5">
        <span className="flex flex-wrap items-center gap-2">
          <span className="text-[13px] font-semibold break-words">
            Add a {e.size} server to pool {pool ?? "that was removed"}
          </span>
          <StatusPill
            tone={e.status === "awaiting_approval" ? "waiting" : "failed"}
          >
            {e.status === "awaiting_approval"
              ? "Waiting for you"
              : (blockWords[reason] ?? "Blocked")}
          </StatusPill>
        </span>
        <span className="text-xs break-words text-muted-foreground">
          {[
            account ?? "A removed account",
            e.region,
            gpus,
            expansionPrice(e),
            `asked ${relative(Date.parse(e.createdAt), now)}`,
          ]
            .filter(Boolean)
            .join(" · ")}
        </span>
        {blockSentence && (
          <span className="text-xs break-words">{blockSentence}</span>
        )}
        {overLimit && (
          <span className="text-xs break-words text-muted-foreground">
            To approve it, raise this limit under Growth policy and save.
          </span>
        )}
        {shownError && (
          <span className="mt-1">
            <CloudError message={shownError} />
          </span>
        )}
      </span>
      {automatic && e.status === "awaiting_approval" && !overLimit && (
        <Button
          size="sm"
          disabled={approve.isPending}
          onClick={() => void act()}
        >
          Approve
        </Button>
      )}
    </Row>
  )
}
