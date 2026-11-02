// What a Cloud's capacity limits count: servers, CPU, memory and storage,
// GPUs by kind, and hosted frontends per target. Used is what the limit
// counts, provisioned plus reserved. Reserved is what deployments hold for
// servers still on their way; the part a provider is already creating
// reads as "starting" inside it, never added on top. Anything Loomplane
// couldn't read says "unknown", never 0.
import {
  CpuIcon,
  GlobeIcon,
  GpuIcon,
  HardDriveIcon,
  MemoryStickIcon,
  ServerIcon,
} from "lucide-react"
import { useState, type ReactNode } from "react"

import { messageOf } from "@/client/errors"
import {
  useCloudCapacityGetPolicy,
  useCloudCapacityRefresh,
  useCloudCapacityUsage,
  useCloudServersList,
  useOrganizationCloudTargetsList,
} from "@/client/forge/loomplane/hooks"
import type {
  FrontendUsage,
  GPUUsage,
  Server,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import {
  Cell,
  CloudCallout,
  CloudTable,
  CloudTableRow,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import { CloudSectionHead } from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { relative } from "@/lib/format"
import { gpuTitle, gpusUnread } from "@/lib/gpu"
import { useNow } from "@/lib/use-runs"

const dimensions = [
  { key: "servers", resource: "servers", label: "Servers", icon: ServerIcon },
  { key: "cpuMillis", resource: "cpu_millis", label: "CPU", icon: CpuIcon },
  {
    key: "memoryBytes",
    resource: "memory_bytes",
    label: "Memory",
    icon: MemoryStickIcon,
  },
  {
    key: "storageBytes",
    resource: "storage_bytes",
    label: "Storage",
    icon: HardDriveIcon,
  },
] as const

const columns = "minmax(0,1fr) minmax(0,1.6fr) minmax(0,0.8fr) minmax(0,0.8fr)"
const head = ["Resource", "Reserved", "Headroom", "Provisioned"]

type Amount = number | null | undefined

const trim = (n: number, digits: number) =>
  Number(n.toFixed(digits)).toLocaleString("en-US")

// CPU reads in vCPU, memory in GiB and storage in GB, the units its limits
// are typed in on this tab.
function amount(resource: string, n: Amount): string {
  if (typeof n !== "number") return "unknown"
  if (resource === "cpu_millis") return `${trim(n / 1000, 2)} vCPU`
  if (resource === "memory_bytes") return `${trim(n / 2 ** 30, 2)} GiB`
  if (resource === "storage_bytes") return `${trim(n / 1e9, 1)} GB`
  return n.toLocaleString("en-US")
}

// used is what a limit counts: provisioned plus reserved. An unknown
// provisioned amount makes it unknown, never just the reserved part.
function used(provisioned: Amount, reserved: Amount): number | undefined {
  return typeof provisioned === "number" && typeof reserved === "number"
    ? provisioned + reserved
    : undefined
}

export function CapacityUsage({ cloudId }: { cloudId: string }) {
  const usage = useQuery(useCloudCapacityUsage, { path: { cloudId } })
  // Usage carries no limits, so the ceilings come from the policy view (the
  // CapacityPolicyView:{cloudId} cache, shared with the policy editor).
  const policy = useQuery(useCloudCapacityGetPolicy, { path: { cloudId } })
  const refresh = useMutation(useCloudCapacityRefresh)
  const [error, setError] = useState("")
  const now = useNow(30_000)
  const failed = usage.status === "error" ? usage : policy
  if (failed.status === "error")
    return (
      <QueryError
        error={failed.error}
        fallback="Capacity usage couldn't be loaded."
        retry={failed.refetch}
      />
    )
  if (!usage.data || !policy.data)
    return <Skeleton className="h-32 rounded-xl" />
  const u = usage.data
  // Only a policy never saved has no ceilings. Once saved, every ceiling is
  // enforced, enabled or not, and 0 is a real limit.
  const saved = policy.data.policy
  const limits = saved && saved.revision > 0 ? saved.limits : undefined
  const at = Date.parse(u.checkedAt)
  // The zero time is what the API sends for a Cloud never checked.
  const checked =
    Number.isFinite(at) && at > 0
      ? `checked ${relative(at, now)}`
      : "never checked"
  const incomplete = dimensions.some(
    (d) => u.provisioned?.[d.key] == null || u.headroom?.[d.key] == null
  )
  const refreshButton = (
    <Button
      size="sm"
      variant="outline"
      disabled={refresh.isPending}
      onClick={() => {
        setError("")
        refresh
          .mutateAsync({ path: { cloudId } })
          .catch((e: unknown) =>
            setError(messageOf(e, "Usage couldn't be refreshed."))
          )
      }}
    >
      {refresh.isPending && <Spinner />}
      Refresh
    </Button>
  )
  return (
    <section className="grid gap-2.5" aria-label="Capacity usage">
      <CloudSectionHead
        title="Usage"
        description={`What this Cloud's servers and reservations count against its limits, ${checked}. Used is what's provisioned plus what's reserved.`}
      >
        {u.fresh && refreshButton}
      </CloudSectionHead>
      {!u.fresh && (
        <CloudCallout
          title={incomplete ? "Usage is incomplete" : "Usage is out of date"}
          action={refreshButton}
        >
          {incomplete
            ? "Unknown amounts need a fresh observation before Loomplane counts them."
            : "Refresh to count what this Cloud's servers hold now."}
        </CloudCallout>
      )}
      {error && <CloudCallout tone="error" title={error} />}
      <CloudTable label="Capacity usage" columns={columns} head={head}>
        {dimensions.map((d) => (
          <ResourceRow
            key={d.key}
            icon={<d.icon />}
            label={d.label}
            resource={d.resource}
            reserved={u.reserved[d.key]}
            pending={u.pending[d.key]}
            provisioned={u.provisioned[d.key]}
            headroom={u.headroom[d.key]}
            limit={limits?.[d.key]}
          />
        ))}
        {u.gpus.length > 0 && <GpuRows cloudId={cloudId} gpus={u.gpus} />}
        {u.frontends.length > 0 && (
          <FrontendRows cloudId={cloudId} frontends={u.frontends} />
        )}
      </CloudTable>
    </section>
  )
}

/** Meter is how much of a limit is counted, full at the limit. */
function Meter({
  label,
  value,
  max,
}: {
  label: string
  value: number | undefined
  max: number | null | undefined
}) {
  if (value === undefined || max === undefined || max === null) return null
  const share = max > 0 ? Math.min(100, (value / max) * 100) : 100
  return (
    <span
      role="meter"
      aria-label={`${label} used of its limit`}
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
  )
}

/** Reserved is a row's Reserved cell: what's counted, a meter, and why. */
function Reserved({
  label,
  text,
  detail,
  value,
  max,
}: {
  label: string
  text: string
  detail?: string
  value: number | undefined
  max: number | null | undefined
}) {
  return (
    <Cell className="grid gap-1 tabular-nums">
      <span>{text}</span>
      <Meter label={label} value={value} max={max} />
      {detail && (
        <span className="text-xs text-muted-foreground">{detail}</span>
      )}
    </Cell>
  )
}

/** Note is a sentence under a row, across the whole table. */
function Note({ children }: { children: ReactNode }) {
  return (
    <Cell className="col-span-full -mt-1 text-xs break-words text-muted-foreground">
      {children}
    </Cell>
  )
}

function ResourceRow({
  icon,
  label,
  resource,
  reserved,
  pending,
  provisioned,
  headroom,
  limit,
}: {
  icon: ReactNode
  label: string
  resource: string
  reserved: Amount
  pending: Amount
  provisioned: Amount
  headroom: Amount
  limit: number | undefined
}) {
  const counted = used(provisioned, reserved)
  const of = limit === undefined ? "no ceiling" : amount(resource, limit)
  return (
    <CloudTableRow>
      <NameCell tile={icon} name={label} />
      <Reserved
        label={label}
        text={`${amount(resource, counted)} used of ${of}`}
        detail={`${amount(resource, reserved)} reserved, ${amount(resource, pending)} starting`}
        value={counted}
        max={limit}
      />
      <Cell className="tabular-nums">
        {limit === undefined ? (
          <span className="text-muted-foreground">No ceiling</span>
        ) : (
          amount(resource, headroom)
        )}
      </Cell>
      <Cell className="tabular-nums">{amount(resource, provisioned)}</Cell>
      {limit === 0 && <Note>Can&apos;t grow: set a ceiling above zero.</Note>}
    </CloudTableRow>
  )
}

function GpuRows({ cloudId, gpus }: { cloudId: string; gpus: GPUUsage[] }) {
  const unknown = gpus.some((g) => g.provisioned === null)
  // Only read the servers when a count is unknown, to name the ones at fault.
  const servers = useQueries(
    useCloudServersList,
    unknown ? [{ path: { cloudId } }] : []
  )
  const unread = (servers.states[0]?.data?.servers ?? [])
    .filter((s): s is Server => !!s && gpusUnread(s))
    .map((s) => s.name)
  return gpus.map((g) => {
    const label = `${gpuTitle(g.kind)} GPUs`
    const counted = used(g.provisioned, g.reserved)
    return (
      <CloudTableRow key={g.kind}>
        <NameCell tile={<GpuIcon />} name={label} />
        <Reserved
          label={label}
          text={`${counted ?? "unknown"} used of ${g.limit ?? "no ceiling"}`}
          detail={`${g.reserved} reserved, ${g.pending} starting`}
          value={counted}
          max={g.limit}
        />
        <Cell className="tabular-nums">
          {g.limit === null ? (
            <span className="text-muted-foreground">No ceiling</span>
          ) : counted === undefined ? (
            "unknown"
          ) : (
            Math.max(0, g.limit - counted)
          )}
        </Cell>
        <Cell className="tabular-nums">{g.provisioned ?? "unknown"}</Cell>
        {g.provisioned === null && unread.length > 0 && (
          <Note>{`Loomplane couldn't count GPUs on ${unread.join(", ")}.`}</Note>
        )}
        {(g.limit === null || g.limit === 0) && (
          <Note>Can&apos;t grow: set a GPU ceiling for this kind.</Note>
        )}
      </CloudTableRow>
    )
  })
}

function frontendText(f: FrontendUsage): string {
  if (f.maximum === undefined || f.maximum === null) return "Unlimited"
  if (f.maximum === 0) return "No new frontends"
  return `${f.counted} of ${f.maximum}`
}

function FrontendRows({
  cloudId,
  frontends,
}: {
  cloudId: string
  frontends: FrontendUsage[]
}) {
  const targets = useQuery(useOrganizationCloudTargetsList, {
    path: { cloudId },
  })
  const name = (id: string) => {
    const t = targets.data?.targets.find((t) => t?.id === id)
    return t?.displayName || t?.name || id
  }
  return frontends.map((f, i) => {
    const label = `Frontends on ${name(f.targetId)}`
    const max = f.maximum ?? null
    return (
      <CloudTableRow key={f.targetId}>
        <NameCell tile={<GlobeIcon />} name={label} />
        <Reserved
          label={label}
          text={frontendText(f)}
          value={f.counted}
          max={max}
        />
        <Cell className="tabular-nums">
          {max === null ? (
            <span className="text-muted-foreground">No ceiling</span>
          ) : (
            Math.max(0, max - f.counted)
          )}
        </Cell>
        <Cell className="tabular-nums">{f.active}</Cell>
        {i === 0 && targets.status === "error" && (
          <Note>
            {messageOf(targets.error, "Target names couldn't be loaded.")}
          </Note>
        )}
      </CloudTableRow>
    )
  })
}
