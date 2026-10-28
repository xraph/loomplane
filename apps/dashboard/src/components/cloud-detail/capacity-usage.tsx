// What a Cloud's capacity limits count: servers, CPU, memory and storage,
// GPUs by kind, and hosted frontends per target. Used is what the limit
// counts, provisioned plus reserved. Reserved is what deployments hold for
// servers still on their way; the part a provider is already creating
// reads as "starting" inside it, never added on top. Anything Loomplane
// couldn't read says "unknown", never 0.
import { useState } from "react"

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
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { formatAmount } from "@/lib/capacity"
import { relative } from "@/lib/format"
import { gpuTitle, gpusUnread } from "@/lib/gpu"
import { useNow } from "@/lib/use-runs"
import {
  CloudError,
  CloudLoading,
  CloudSectionHead,
  Row,
  Rows,
} from "@/components/cloud-targets/infrastructure-ui"

const dimensions = [
  { key: "servers", resource: "servers", label: "Servers" },
  { key: "cpuMillis", resource: "cpu_millis", label: "CPU" },
  { key: "memoryBytes", resource: "memory_bytes", label: "Memory" },
  { key: "storageBytes", resource: "storage_bytes", label: "Storage" },
] as const

type Amount = number | null | undefined

// Storage reads in GB, the unit its limit is typed in on this tab.
function amount(resource: string, n: Amount): string {
  if (typeof n !== "number") return "unknown"
  if (resource === "storage_bytes")
    return `${Number((n / 1e9).toFixed(1)).toLocaleString("en-US")} GB`
  return formatAmount(resource, n)
}

export function CapacityUsage({ cloudId }: { cloudId: string }) {
  const usage = useQuery(useCloudCapacityUsage, { path: { cloudId } })
  // Usage carries no limits, so the ceilings come from the policy view (the
  // CapacityPolicyView:{cloudId} cache, shared with the policy editor).
  const policy = useQuery(useCloudCapacityGetPolicy, { path: { cloudId } })
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
    return <CloudLoading>Loading capacity usage...</CloudLoading>
  const u = usage.data
  // Only a policy never saved has no ceilings. Once saved, every ceiling is
  // enforced, enabled or not, and 0 is a real limit.
  const saved = policy.data.policy
  const limits = saved && saved.revision > 0 ? saved.limits : undefined
  return (
    <section className="grid gap-3" aria-label="Capacity usage">
      <CloudSectionHead
        title="Capacity usage"
        description="What this Cloud's servers and reservations count against its limits. Used is what's provisioned plus what's reserved; starting is the part of reserved a provider is already creating."
      />
      {!u.fresh && <StaleUsage cloudId={cloudId} checkedAt={u.checkedAt} />}
      <Rows>
        {dimensions.map((d) => (
          <ResourceRow
            key={d.key}
            label={d.label}
            resource={d.resource}
            reserved={u.reserved[d.key]}
            pending={u.pending[d.key]}
            provisioned={u.provisioned[d.key]}
            headroom={u.headroom[d.key]}
            limit={limits?.[d.key]}
          />
        ))}
      </Rows>
      {u.gpus.length > 0 && <GpuRows cloudId={cloudId} gpus={u.gpus} />}
      {u.frontends.length > 0 && (
        <FrontendRows cloudId={cloudId} frontends={u.frontends} />
      )}
    </section>
  )
}

function StaleUsage({
  cloudId,
  checkedAt,
}: {
  cloudId: string
  checkedAt: string
}) {
  const now = useNow(30_000)
  const refresh = useMutation(useCloudCapacityRefresh)
  const [error, setError] = useState("")
  const at = Date.parse(checkedAt)
  // The zero time is what the API sends for a Cloud never checked.
  const checked =
    Number.isFinite(at) && at > 0
      ? `checked ${relative(at, now)}`
      : "never checked"
  return (
    <Alert>
      <AlertDescription className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <span>{`Usage is out of date (${checked}).`}</span>
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
        {error && <span className="basis-full">{error}</span>}
      </AlertDescription>
    </Alert>
  )
}

// used is what a limit counts: provisioned plus reserved. An unknown
// provisioned amount makes it unknown, never just the reserved part.
function used(provisioned: Amount, reserved: Amount): number | undefined {
  return typeof provisioned === "number" && typeof reserved === "number"
    ? provisioned + reserved
    : undefined
}

function ResourceRow({
  label,
  resource,
  reserved,
  pending,
  provisioned,
  headroom,
  limit,
}: {
  label: string
  resource: string
  reserved: Amount
  pending: Amount
  provisioned: Amount
  headroom: Amount
  limit: number | undefined
}) {
  const of = limit === undefined ? "no ceiling" : amount(resource, limit)
  return (
    <Row className="flex-wrap items-start">
      <span className="w-20 shrink-0 text-[13px] font-medium">{label}</span>
      <div className="grid min-w-0 flex-[1_1_220px] gap-0.5">
        <span className="flex flex-wrap items-baseline gap-x-3 text-[13px] tabular-nums">
          <span>
            {`${amount(resource, used(provisioned, reserved))} used of ${of} (${amount(resource, reserved)} reserved, ${amount(resource, pending)} starting)`}
          </span>
          {limit !== undefined && (
            <span className="text-muted-foreground">{`Headroom: ${amount(resource, headroom)}`}</span>
          )}
        </span>
        <span className="text-xs text-muted-foreground tabular-nums">
          {`Provisioned: ${amount(resource, provisioned)}`}
        </span>
        {limit === 0 && (
          <span className="text-xs text-muted-foreground">
            Can&apos;t grow: set a ceiling above zero.
          </span>
        )}
      </div>
    </Row>
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
  return (
    <div className="grid gap-2">
      <h3 className="text-[13px] font-medium">GPUs</h3>
      <Rows>
        {gpus.map((g) => (
          <Row key={g.kind} className="flex-wrap items-start">
            <div className="grid min-w-0 flex-1 gap-0.5">
              <span className="text-[13px] tabular-nums">
                {`${gpuTitle(g.kind)}: ${used(g.provisioned, g.reserved) ?? "unknown"} used of ${g.limit ?? "no ceiling"} (${g.reserved} reserved, ${g.pending} starting)`}
              </span>
              <span className="text-xs text-muted-foreground tabular-nums">
                {`Provisioned: ${g.provisioned ?? "unknown"}`}
              </span>
              {g.provisioned === null && unread.length > 0 && (
                <span className="text-xs break-words text-muted-foreground">
                  {`Loomplane couldn't count GPUs on ${unread.join(", ")}.`}
                </span>
              )}
              {(g.limit === null || g.limit === 0) && (
                <span className="text-xs text-muted-foreground">
                  Can&apos;t grow: set a GPU ceiling for this kind.
                </span>
              )}
            </div>
          </Row>
        ))}
      </Rows>
    </div>
  )
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
  return (
    <div className="grid gap-2">
      <h3 className="text-[13px] font-medium">Frontends</h3>
      <Rows>
        {frontends.map((f) => (
          <Row key={f.targetId} className="flex-wrap">
            <span className="min-w-0 text-[13px] break-all">
              {name(f.targetId)}
            </span>
            <span className="text-[13px] text-muted-foreground tabular-nums">
              {frontendText(f)}
            </span>
          </Row>
        ))}
      </Rows>
      {targets.status === "error" && (
        <CloudError
          message={messageOf(targets.error, "Target names couldn't be loaded.")}
        />
      )}
    </div>
  )
}
