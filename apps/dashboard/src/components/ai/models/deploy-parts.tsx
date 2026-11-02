// The pieces the deploy and move dialogs share: where a model runs, the
// GPU server Loomplane would create for it and its "Review server" step,
// and what the preview says it costs.
import { InfoIcon, LockIcon } from "lucide-react"
import { useId, type ReactNode } from "react"

import { statusOf } from "@/client/errors"
import { useConnectionsList } from "@/client/forge/loomplane/hooks"
import type {
  DeployInputBody,
  DeployPreview,
  GPUTarget,
  Operation,
  Problem,
  QuantizationView,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { VerificationBadge } from "@/components/ai/models/verification-badge"
import { GpuTargets } from "@/components/gpu-targets"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { cloudLabel } from "@/lib/clouds"
import { acceleratorLabel, formatHourly, gpuTitle, priceKnown } from "@/lib/gpu"
import {
  deployCost,
  engineName,
  fitOn,
  fitsStatus,
  gpuMemoryOf,
  weightSize,
  type RunsOn,
} from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

// The problem a preview has for a GPU server someone who isn't an admin
// would create.
export const serverAdmin = "model-server-needs-admin"

export type Target =
  | { runsOn: "gpu"; gpu: GPUTarget; region?: string }
  | { runsOn: "cpu"; connectionId: string; name: string }

// A deploy whose server waits for capacity, or that the capacity policy
// refused, with the Cloud whose Capacity tab says why.
export type CapacityOutcome = {
  message: string
  cloudId?: string
  paused: boolean
  /** The model's name, once it has one. */
  resource?: string
}

// Why a deploy didn't go through, and what to do next when that isn't
// the server's sentence.
export type DeployError = {
  message: string
  next?: string
  /** The page of the model it's about, when it may have gone ahead. */
  model?: { projectId: string; environmentId: string; resource: string }
}

/** targetChoice is the deploy body's target for what was chosen. */
export function targetChoice(t: Target): DeployInputBody["target"] {
  if (t.runsOn === "cpu")
    return { source: "connection", connectionId: t.connectionId }
  const gpu = { kind: t.gpu.accelerator.kind, count: 1 }
  if (t.gpu.source === "server_size")
    return {
      source: "server_size",
      connectionId: t.gpu.connectionId,
      gpu,
      region: t.region,
      sizeId: t.gpu.sizeId,
    }
  return { source: "connection", connectionId: t.gpu.connectionId, gpu }
}

/**
 * minMemory is the GPU memory the list asks for: the chosen
 * quantization's, or the smallest of those offered while none is chosen,
 * so every GPU one of them fits on shows.
 */
export function minMemory(offered: QuantizationView[], quant: string) {
  const chosen = offered.find((q) => q.id === quant)
  const needs = (chosen ? [chosen] : offered)
    .map(gpuMemoryOf)
    .filter((n): n is number => n !== undefined)
  return needs.length ? Math.min(...needs) : undefined
}

/**
 * refusal says why no quantization offered can run on a GPU kind that has
 * the memory, such as one that failed there in qualification.
 */
export function refusal(
  offered: QuantizationView[],
  quant: string,
  kind: string
): string | undefined {
  const chosen = offered.find((q) => q.id === quant)
  const fits = (chosen ? [chosen] : offered).map((q) => fitOn(q, kind))
  if (fits.some((f) => !f || fitsStatus(f))) return undefined
  const f = fits.find((f) => f?.reason) ?? fits[0]
  if (f?.status === "failed")
    return `Loomplane's qualification couldn't serve it on ${gpuTitle(kind)}.`
  return f?.reason || `It doesn't fit on ${gpuTitle(kind)}.`
}

/* ------------------------------------------------------------------------ */

export function Section({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="grid min-w-0 gap-2">
      <h3 id={id} className="text-[13px] font-semibold">
        {title}
      </h3>
      {children}
    </section>
  )
}

export function Choice({
  label,
  value,
  items,
  onChange,
}: {
  label: string
  value: string
  items: { value: string; label: string; disabled?: boolean }[]
  onChange: (v: string) => void
}) {
  return (
    <Select
      items={items}
      value={value}
      onValueChange={(v) => v && onChange(String(v))}
    >
      <SelectTrigger aria-label={label} className="h-9 w-full min-w-0">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {items.map((i) => (
          <SelectItem key={i.value} value={i.value} disabled={i.disabled}>
            {i.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function Segmented<T extends string>({
  label,
  value,
  items,
  onChange,
}: {
  label: string
  value: T
  items: { value: T; label: string }[]
  onChange: (v: T) => void
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex w-fit rounded-[9px] bg-muted p-0.5"
    >
      {items.map((i) => (
        <button
          key={i.value}
          type="button"
          role="radio"
          aria-checked={value === i.value}
          onClick={() => onChange(i.value)}
          className={cn(
            "h-7 rounded-[7px] px-3 text-[12.5px] font-medium outline-none focus-visible:ring-3 focus-visible:ring-ring/50",
            value === i.value
              ? "bg-background text-foreground shadow-xs"
              : "text-muted-foreground hover:text-foreground"
          )}
        >
          {i.label}
        </button>
      ))}
    </div>
  )
}

export function NumberField({
  label,
  value,
  placeholder,
  max,
  prefix,
  decimal,
  onChange,
}: {
  label: string
  value: string
  placeholder?: string
  max?: number
  prefix?: string
  decimal?: boolean
  onChange: (v: string) => void
}) {
  return (
    <div className="flex h-9 min-w-0 items-center gap-1.5 rounded-[9px] border border-input bg-background px-2.5 focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/40">
      {prefix && (
        <span aria-hidden="true" className="text-muted-foreground">
          {prefix}
        </span>
      )}
      <input
        aria-label={label}
        type={decimal ? "text" : "number"}
        inputMode={decimal ? "decimal" : "numeric"}
        min={decimal ? undefined : 1}
        max={max}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        autoComplete="off"
        className="h-full w-full min-w-0 bg-transparent font-mono text-[13px] tabular-nums outline-none placeholder:font-sans placeholder:text-muted-foreground"
      />
    </div>
  )
}

export function Note({
  tone,
  children,
}: {
  tone: "weld" | "failed" | "quiet"
  children: ReactNode
}) {
  return (
    <p
      className={cn(
        "flex items-start gap-1.5 text-[12.5px] leading-snug",
        tone === "weld" && "text-weld-ink",
        tone === "failed" && "text-destructive",
        tone === "quiet" && "text-muted-foreground"
      )}
    >
      <InfoIcon aria-hidden="true" className="mt-px size-3.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </p>
  )
}

export function ChosenGpu({
  target,
  onRegion,
  onChange,
}: {
  target: Extract<Target, { runsOn: "gpu" }>
  onRegion: (region: string) => void
  onChange: () => void
}) {
  const t = target.gpu
  const size = t.source === "server_size"
  const regions = t.regions ?? []
  return (
    <div className="grid gap-2 rounded-xl p-3 ring-1 ring-border">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="grid min-w-0 gap-0.5">
          <span
            className={cn(
              "truncate font-medium",
              size && "font-mono text-[12.5px]"
            )}
          >
            {size ? `New server: ${t.name}` : t.name}
          </span>
          <span className="text-[12.5px] text-muted-foreground">
            {acceleratorLabel(t.accelerator)}
            {!size && ` · ${cloudLabel(t.cloud)}`}
          </span>
        </div>
        <Button size="sm" variant="outline" onClick={onChange}>
          Change
        </Button>
      </div>
      {size && regions.length > 1 && (
        <Choice
          label="Region"
          value={target.region ?? regions[0]}
          items={regions.map((r) => ({ value: r, label: r }))}
          onChange={onRegion}
        />
      )}
    </div>
  )
}

export function CpuTargets({
  value,
  onChange,
}: {
  value: string
  onChange: (connectionId: string, name: string) => void
}) {
  const connections = useQuery(useConnectionsList)
  if (connections.status === "error")
    return (
      <QueryError
        error={connections.error}
        fallback="Servers and clusters couldn't be loaded."
        retry={connections.refetch}
      />
    )
  if (!connections.data) return <Skeleton className="h-20 w-full rounded-xl" />
  const ready = connections.data.connections.filter(
    (c) =>
      (c.cloud === "server" || c.cloud === "kubernetes") && c.status === "ready"
  )
  if (ready.length === 0)
    return (
      <p className="text-[13px] text-muted-foreground">
        No server or cluster is ready. Connect one in{" "}
        <Link to="/infrastructure" className="text-primary hover:underline">
          Infrastructure
        </Link>
        , then deploy the model.
      </p>
    )
  return (
    <div
      role="radiogroup"
      aria-label="Server or cluster"
      className="grid gap-1.5"
    >
      {ready.map((c) => (
        <label
          key={c.id}
          className="flex min-w-0 items-center gap-2.5 rounded-[9px] px-2.5 py-2 text-[13px] ring-1 ring-border has-checked:ring-ring"
        >
          <input
            type="radio"
            className="accent-primary"
            name="cpu-target"
            value={c.id}
            checked={value === c.id}
            onChange={() => onChange(c.id, c.name)}
          />
          <span className="truncate font-medium">{c.name}</span>
          <span className="text-muted-foreground">{cloudLabel(c.cloud)}</span>
        </label>
      ))}
    </div>
  )
}

export function ServerReview({
  admin,
  preview,
  fresh,
  reviewed,
  reviewing,
  error,
  action,
  onReview,
}: {
  action?: string
  admin: boolean
  preview?: DeployPreview
  fresh: boolean
  reviewed?: DeployPreview
  reviewing: boolean
  error: string
  onReview: () => void
}) {
  const server = reviewed?.server
  // The review is its own step: only once the plain preview has no
  // problems, and only for admins.
  const reviewable =
    admin && fresh && !!preview?.serverReviewNeeded && !preview.problems.length
  const notAdmin =
    preview?.problems.find((p) => p.code === serverAdmin)?.message ??
    (admin
      ? undefined
      : "An admin creates GPU servers. Ask one to deploy this, or pick a server you have.")
  return (
    <section
      aria-label="GPU server"
      className="grid gap-2 rounded-xl bg-subtle p-3 ring-1 ring-border"
    >
      {server ? (
        <ReviewedServer server={server} action={action} />
      ) : (
        <>
          <p className="text-[13px]">
            Loomplane creates a GPU server for this model first. Review it to
            see what the provider charges before anything is created.
          </p>
          {notAdmin && <Note tone="weld">{notAdmin}</Note>}
          <Button
            size="sm"
            variant="outline"
            className="w-fit"
            disabled={!reviewable || reviewing}
            onClick={onReview}
          >
            {reviewing && <Spinner />}
            Review server
          </Button>
        </>
      )}
      {error && <Note tone="failed">{error}</Note>}
    </section>
  )
}

export function ReviewedServer({
  server,
  action = "Deploy",
}: {
  server: Operation
  /** What the review is for: "Deploy" or "Plan the move". */
  action?: string
}) {
  // The provider's price for the whole server. Unknown is never $0.
  const price = priceKnown(server.estimatedHourlyUsd, server.hourlyPriceKnown)
    ? formatHourly(server.estimatedHourlyUsd, true)
    : "Price unknown"
  return (
    <div className="grid gap-1">
      <p className="text-[13px] font-medium" data-testid="server-review">
        Create <span className="font-mono">{server.input.name}</span> in{" "}
        {server.input.region} · {price}
      </p>
      <p className="text-[12.5px] text-muted-foreground">
        It bills from the moment it&apos;s created until you delete it in
        Infrastructure, whether or not the model gets traffic. {action} within
        five minutes of this review, or review it again.
      </p>
    </div>
  )
}

export function Summary({
  preview: pv,
  stale,
  working,
  error,
  capacity,
  deployError,
}: {
  preview?: DeployPreview
  stale: boolean
  working: boolean
  error?: string
  capacity?: CapacityOutcome
  deployError?: DeployError
}) {
  if (!pv && !error && !working)
    return (
      <p className="text-[12.5px] text-muted-foreground">
        Pick where it runs to see what it costs.
      </p>
    )
  const cost = pv && deployCost(pv.cost, pv.targetName)
  return (
    <div
      aria-live="polite"
      aria-busy={working}
      className={cn(
        "grid gap-2 border-t pt-4 transition-opacity",
        stale && "opacity-60"
      )}
    >
      {!pv && working && <Skeleton className="h-5 w-56" />}
      {cost && (
        <div className="grid gap-0.5">
          <p
            className={cn(
              "text-[14px] font-semibold",
              cost.kind === "known" || cost.kind === "estimate"
                ? "tabular-nums"
                : ""
            )}
            data-testid="deploy-cost"
          >
            {cost.line}
            {cost.kind === "estimate" && (
              <span className="ml-1.5 text-[12px] font-normal text-muted-foreground">
                estimate
              </span>
            )}
          </p>
          {cost.note && (
            <p className="text-[12.5px] text-muted-foreground">{cost.note}</p>
          )}
        </div>
      )}
      {/* Adding an environment, the Where section says what it adds. */}
      {!pv?.addsTo && pv?.environments[0]?.message && (
        <p className="text-[12.5px] text-muted-foreground">
          {pv.environments[0].message}
        </p>
      )}
      {pv?.gated && pv.tokenReady && (
        <p className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
          <LockIcon aria-hidden="true" className="size-3.5 shrink-0" />
          Gated on Hugging Face. The workspace&apos;s token can download it.
        </p>
      )}
      {pv?.problems
        // The GPU server section says this one, beside its button.
        .filter((p) => p.code !== serverAdmin)
        .map((p) => (
          <ProblemLine key={`${p.code}:${p.message}`} problem={p} />
        ))}
      {error && <Note tone="failed">{error}</Note>}
      {capacity && !capacity.paused && (
        <div className="grid gap-1">
          <Note tone="failed">{capacity.message}</Note>
          <CapacityLink cloudId={capacity.cloudId} indent />
        </div>
      )}
      {deployError && (
        <Note tone="failed">
          {deployError.message}
          {deployError.next && (
            <span className="block text-foreground">{deployError.next}</span>
          )}
          {deployError.model && (
            <Link
              to="/projects/$id/ai/models/$resource"
              params={{
                id: deployError.model.projectId,
                resource: deployError.model.resource,
              }}
              search={{ environment: deployError.model.environmentId }}
              className="block w-fit font-medium text-primary hover:underline"
            >
              Open the model page
            </Link>
          )}
        </Note>
      )}
    </div>
  )
}

export function ProblemLine({ problem }: { problem: Problem }) {
  const { can } = useOrganization()
  const settings = problem.code === "model-gated"
  return (
    <div className="grid gap-1">
      <Note tone="weld">{problem.message}</Note>
      {settings &&
        (can("ai:manage") ? (
          <Link
            to="/ai/settings"
            className="ml-5 w-fit text-[12.5px] font-medium text-primary hover:underline"
          >
            Open AI settings
          </Link>
        ) : (
          <p className="ml-5 text-[12.5px] text-muted-foreground">
            Ask a workspace admin to add a Hugging Face token in AI settings.
          </p>
        ))}
    </div>
  )
}

export function CapacityLink({
  cloudId,
  indent,
}: {
  cloudId?: string
  /** Lines it up under a note's text, past its icon. */
  indent?: boolean
}) {
  const { can } = useOrganization()
  if (!cloudId || !can("organization_cloud:manage")) return null
  return (
    <Link
      to="/clouds/$id"
      params={{ id: cloudId }}
      search={{ tab: "capacity" }}
      className={cn(
        "w-fit text-[12.5px] font-medium text-primary hover:underline",
        indent && "ml-5"
      )}
    >
      Open the Cloud&apos;s Capacity tab
    </Link>
  )
}

/** said is the server's sentence as it wrote it, else fallback. */
export function said(err: unknown, fallback: string): string {
  const raw = err instanceof Error ? err.message.trim() : ""
  if (!raw || /^HTTP \d+/.test(raw) || statusOf(err) >= 500) return fallback
  return /[.!?]$/.test(raw) ? raw : `${raw}.`
}

/**
 * Placement is where the model runs: a GPU, listed with what fits and what
 * it costs, or a ready server or cluster for an engine that runs on CPU.
 */
export function Placement({
  options,
  runsOn,
  target,
  offered,
  quant,
  onRunsOn,
  onTarget,
}: {
  options: RunsOn[]
  runsOn: RunsOn
  target?: Target
  offered: QuantizationView[]
  quant: string
  onRunsOn: (v: RunsOn) => void
  onTarget: (t: Target | undefined) => void
}) {
  return (
    <Section title={runsOn === "gpu" ? "GPU" : "Server"}>
      {options.length > 1 && (
        <Segmented
          label="Runs on"
          value={runsOn}
          items={[
            { value: "gpu", label: "GPU" },
            { value: "cpu", label: "CPU" },
          ]}
          onChange={onRunsOn}
        />
      )}
      {runsOn === "gpu" ? (
        target?.runsOn === "gpu" ? (
          <ChosenGpu
            target={target}
            onRegion={(region) => onTarget({ ...target, region })}
            onChange={() => onTarget(undefined)}
          />
        ) : (
          <GpuTargets
            minMemoryGib={minMemory(offered, quant)}
            count={1}
            onSelect={(t) =>
              onTarget({ runsOn: "gpu", gpu: t, region: t.regions?.[0] })
            }
            refuse={(t) => refusal(offered, quant, t.accelerator.kind)}
          />
        )
      ) : (
        <CpuTargets
          value={target?.runsOn === "cpu" ? target.connectionId : ""}
          onChange={(connectionId, name) =>
            onTarget({ runsOn: "cpu", connectionId, name })
          }
        />
      )}
    </Section>
  )
}

/**
 * QuantizationChoice lists the quantizations that run where the model
 * goes, each with how sure Loomplane is of it on the chosen GPU. One that
 * doesn't fit there can't be picked. Until one is picked, the preview's
 * choice is checked.
 */
export function QuantizationChoice({
  offered,
  kind,
  quant,
  picked,
  onChange,
}: {
  offered: QuantizationView[]
  /** The chosen GPU kind, or "cpu". */
  kind?: string
  quant: string
  /** What the preview picked when none is chosen. */
  picked?: string
  onChange: (id: string) => void
}) {
  return (
    <Section title="Quantization">
      <div role="radiogroup" aria-label="Quantization" className="grid gap-1.5">
        {offered.map((q) => {
          const fit = kind ? fitOn(q, kind) : undefined
          const off = !!kind && !!fit && !fitsStatus(fit)
          const checked = (quant || picked) === q.id
          return (
            <label
              key={q.id}
              className={cn(
                "flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1 rounded-[9px] px-2.5 py-2 text-[13px] ring-1 ring-border has-checked:ring-ring",
                off && "opacity-60"
              )}
            >
              <input
                type="radio"
                className="accent-primary"
                name="quantization"
                value={q.id}
                checked={checked}
                disabled={off}
                onChange={() => onChange(q.id)}
              />
              <span className="font-mono text-[12.5px] font-medium">
                {q.id}
              </span>
              <span className="text-muted-foreground">
                {engineName(q.engine)} · {weightSize(q.weightBytes)}
              </span>
              {fit && (
                <span className="ml-auto">
                  <VerificationBadge status={fit.status} fit={fit} />
                </span>
              )}
              {off && fit?.reason && (
                <span className="basis-full text-[12px] text-weld-ink">
                  {fit.reason}
                </span>
              )}
            </label>
          )
        })}
      </div>
      {!quant && picked && offered.length > 1 && (
        <p className="text-[12.5px] text-muted-foreground">
          {picked} is the best that fits. Pick another to change it.
        </p>
      )}
    </Section>
  )
}
