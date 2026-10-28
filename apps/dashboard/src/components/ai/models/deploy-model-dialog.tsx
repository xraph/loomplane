// Deploying a model, in one dialog: where it runs, on which GPU or server,
// how big, and what it costs. Every change asks the server for a fresh
// preview, which writes nothing. A GPU server Loomplane would create for
// the model gets its own "Review server" step, for admins only: that review
// asks the provider and records it in Infrastructure, so it's sent from
// that button and never on a change of the form.
import { InfoIcon, LockIcon } from "lucide-react"
import { useEffect, useId, useRef, useState, type ReactNode } from "react"

import { statusOf } from "@/client/errors"
import {
  useAiModelDeploymentsList,
  useAiModelsDeploy,
  useAiModelsEntry,
  useAiModelsPreview,
  useConnectionsList,
  useEnvironmentsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  DeployInputBody,
  DeployPreview,
  DeployResult,
  Environment,
  EntryDetail,
  GPUTarget,
  Operation,
  Problem,
  QuantizationView,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import { VerificationBadge } from "@/components/ai/models/verification-badge"
import { GpuTargets } from "@/components/gpu-targets"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
  runsOnOf,
  weightSize,
  type RunsOn,
} from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

type Props = {
  entryId: string
  /** The entry itself, when the caller has it, such as a pasted repo. */
  detail?: EntryDetail
  quantization?: string
  projectId?: string
  environmentId?: string
  onClose: () => void
  /** Called once the deploy went through, with its plan when it made one. */
  onPlanned?: (planId?: string) => void
}

export function DeployModelDialog(props: Props) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
    >
      <DialogContent className="gap-5 rounded-2xl sm:max-w-[680px]">
        {props.detail ? (
          <DeployForm {...props} entry={props.detail} />
        ) : (
          <EntryLoader {...props} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function Heading({ title }: { title?: string }) {
  return (
    <DialogHeader className="gap-1 pr-6">
      <DialogTitle className="text-[17px] leading-normal font-semibold tracking-[-0.015em]">
        {title ? `Deploy ${title}` : "Deploy a model"}
      </DialogTitle>
      <DialogDescription className="leading-normal">
        It runs on a server or cluster you have, or on a GPU server Loomplane
        creates for it. Nothing changes until you deploy.
      </DialogDescription>
    </DialogHeader>
  )
}

function EntryLoader(props: Props) {
  const entry = useQuery(useAiModelsEntry, { query: { id: props.entryId } })
  if (entry.status === "error")
    return (
      <>
        <Heading />
        {statusOf(entry.error) === 403 ? (
          <p className="text-[13px] text-muted-foreground">
            Ask a workspace admin for access to AI in this workspace.
          </p>
        ) : (
          <QueryError
            error={entry.error}
            fallback="The model couldn't be loaded."
            retry={entry.refetch}
          />
        )}
      </>
    )
  if (!entry.data)
    return (
      <>
        <Heading />
        <div data-testid="deploy-loading" className="grid gap-3">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-32 w-full rounded-xl" />
        </div>
      </>
    )
  return <DeployForm {...props} entry={entry.data} />
}

/* ------------------------------------------------------------------------ */

// The problem a preview has for a GPU server someone who isn't an admin
// would create.
const serverAdmin = "model-server-needs-admin"

type Target =
  | { runsOn: "gpu"; gpu: GPUTarget; region?: string }
  | { runsOn: "cpu"; connectionId: string; name: string }

// A deploy whose server waits for capacity, or that the capacity policy
// refused, with the Cloud whose Capacity tab says why.
type CapacityOutcome = { message: string; cloudId?: string; paused: boolean }

function DeployForm({
  entry,
  quantization,
  projectId,
  environmentId,
  onClose,
  onPlanned,
}: Props & { entry: EntryDetail }) {
  const { can } = useOrganization()
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const previewMutation = useMutation(useAiModelsPreview)
  const deployMutation = useMutation(useAiModelsDeploy)
  const cloudAdmin = can("organization_cloud:manage")

  // Where.
  const projects = useQuery(useProjectsList)
  const projectList = projects.data?.projects ?? []
  const [project, setProject] = useState(projectId ?? "")
  const chosenProject =
    projectList.find((p) => p.id === project)?.id ??
    (projectId || projectList[0]?.id || "")
  const envs = useQueries(
    useEnvironmentsList,
    chosenProject ? [{ path: { id: chosenProject } }] : []
  )
  const envState = envs.states[0]
  const envList = envState?.data?.environments ?? []
  const mayWrite = (e: Environment) =>
    can(e.kind === "production" ? "ai:manage" : "ai:edit")
  const [environment, setEnvironment] = useState(environmentId ?? "")
  const env =
    envList.find((e) => e.id === environment) ??
    envList.find(mayWrite) ??
    envList[0]

  // What runs, and where.
  const options = [
    ...new Set(entry.quantizations.flatMap((q) => runsOnOf(q.engine))),
  ].sort() as RunsOn[]
  const asked = entry.quantizations.find((q) => q.id === quantization)
  const [runsOn, setRunsOn] = useState<RunsOn>(
    (asked && runsOnOf(asked.engine)[0]) ||
      (options.includes("gpu") ? "gpu" : "cpu")
  )
  const offered = entry.quantizations.filter((q) =>
    runsOnOf(q.engine).includes(runsOn)
  )
  const [quant, setQuant] = useState(
    asked && offered.includes(asked) ? asked.id : ""
  )
  const [target, setTarget] = useState<Target>()
  const [context, setContext] = useState("")
  const [budget, setBudget] = useState("")

  const kind = target
    ? target.runsOn === "gpu"
      ? target.gpu.accelerator.kind
      : "cpu"
    : undefined
  const choice = target ? targetChoice(target) : undefined
  const maxModelLen = Number.parseInt(context, 10)
  const body: DeployInputBody | undefined = choice && {
    entryId: entry.id,
    target: choice,
    ...(quant ? { quantization: quant } : {}),
    ...(maxModelLen > 0 ? { maxModelLen } : {}),
    ...(budget.trim() ? { monthlyBudgetUsd: budget.trim() } : {}),
  }
  const path = env && { projectId: chosenProject, environmentId: env.id }
  const key = body && path ? JSON.stringify({ path, body }) : ""

  // The preview of exactly this form, 300 ms after the last change. The
  // last one stays on screen, dimmed, while the next is worked out.
  const [result, setResult] = useState<{
    key: string
    pv?: DeployPreview
    error?: string
  }>()
  const preview = useRef(previewMutation.mutateAsync)
  useEffect(() => {
    preview.current = previewMutation.mutateAsync
  })
  useEffect(() => {
    if (!key) return
    const { path, body } = JSON.parse(key) as {
      path: { projectId: string; environmentId: string }
      body: DeployInputBody
    }
    let current = true
    const timer = window.setTimeout(() => {
      preview
        .current({ path, body })
        .then((pv) => current && setResult({ key, pv: pv as DeployPreview }))
        .catch(
          (err: unknown) =>
            current &&
            setResult({
              key,
              error: said(err, "Loomplane couldn't work this deploy out."),
            })
        )
    }, 300)
    return () => {
      current = false
      window.clearTimeout(timer)
    }
  }, [key])
  const fresh = !!key && result?.key === key
  const pv = key ? result?.pv : undefined

  // The GPU server's review, for exactly this form.
  const [review, setReview] = useState<{ key: string; pv: DeployPreview }>()
  const [reviewing, setReviewing] = useState(false)
  const [reviewError, setReviewError] = useState("")
  const reviewed = review && review.key === key ? review.pv : undefined
  const size = target?.runsOn === "gpu" && target.gpu.source === "server_size"

  const [capacity, setCapacity] = useState<CapacityOutcome>()
  const [error, setError] = useState("")

  const change = (f: () => void) => {
    f()
    setError("")
    setReviewError("")
    setCapacity(undefined)
  }

  async function reviewServer() {
    if (!body || !path || reviewing) return
    setReviewing(true)
    setReviewError("")
    try {
      const r = await previewMutation.mutateAsync({
        path,
        body: { ...body, reviewServer: true },
      })
      setReview({ key, pv: r as DeployPreview })
    } catch (err) {
      setReviewError(said(err, "Loomplane couldn't review the GPU server."))
    } finally {
      setReviewing(false)
    }
  }

  async function deploy() {
    if (!body || !path || deployMutation.isPending) return
    const server = size ? reviewed?.server : undefined
    if (size && !server) return
    setError("")
    setCapacity(undefined)
    try {
      const res = (await deployMutation.mutateAsync({
        path,
        body: server
          ? {
              ...body,
              server: {
                cloudId: server.cloudId,
                operationId: server.id,
                review: server.review,
              },
            }
          : body,
      })) as DeployResult
      invalidate(useAiModelDeploymentsList)
      if (res.planId) {
        onPlanned?.(res.planId)
        void navigate({ to: "/deployments/$id", params: { id: res.planId } })
        return
      }
      if (server && res.deployment?.message) {
        // Approved, and held back by the capacity policy for now.
        setCapacity({
          message: res.deployment.message,
          cloudId: server.cloudId,
          paused: true,
        })
        return
      }
      onPlanned?.(undefined)
      // Until the model page exists, a model waiting for its server shows on
      // the Models page's Deployed tab.
      void navigate({ to: "/ai/models", search: { tab: "deployed" } })
    } catch (err) {
      const code = (err as { code?: unknown }).code
      if (code === "model-capacity") {
        setCapacity({
          message: said(err, "The cloud's capacity policy refused the server."),
          cloudId: server?.cloudId,
          paused: false,
        })
        setReview(undefined)
        return
      }
      if (code === "model-server-review") setReview(undefined)
      setError(said(err, "The model wasn't deployed."))
    }
  }

  if (capacity?.paused)
    return (
      <>
        <Heading title={entry.title} />
        <div className="grid gap-3">
          <StatusPill tone="waiting">Waiting for a server</StatusPill>
          <p className="text-[13.5px] text-pretty">{capacity.message}</p>
          <p className="text-[13px] text-muted-foreground">
            The model deploys once the server is up. The server bills from then
            until you delete it in Infrastructure.
          </p>
          <CapacityLink cloudId={capacity.cloudId} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
          <Button
            onClick={() => {
              onPlanned?.(undefined)
              void navigate({ to: "/ai/models", search: { tab: "deployed" } })
            }}
          >
            See it on the Models page
          </Button>
        </DialogFooter>
      </>
    )

  const blockedEnv = env && !mayWrite(env)
  const shown = size ? (reviewed ?? pv) : pv
  const ready = size
    ? !!reviewed?.server && reviewed.canDeploy
    : fresh && !!pv?.canDeploy
  const canDeploy = ready && !blockedEnv && !deployMutation.isPending

  return (
    <>
      <Heading title={entry.title} />
      <div className="grid min-w-0 gap-5">
        <Section title="Where">
          {projects.status === "error" ? (
            <QueryError
              error={projects.error}
              fallback="Projects couldn't be loaded."
              retry={projects.refetch}
            />
          ) : !projects.data ? (
            <Skeleton className="h-9 w-full" />
          ) : projectList.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">
              Create a project first. A model runs in one of its environments.
            </p>
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              <Choice
                label="Project"
                value={chosenProject}
                items={projectList.map((p) => ({ value: p.id, label: p.name }))}
                onChange={(v) =>
                  change(() => {
                    setProject(v)
                    setEnvironment("")
                  })
                }
              />
              {envState?.status === "error" ? (
                <QueryError
                  error={envState.error}
                  fallback="Environments couldn't be loaded."
                  retry={envs.refetch}
                />
              ) : !envState?.data ? (
                <Skeleton className="h-9 w-full" />
              ) : (
                <Choice
                  label="Environment"
                  value={env?.id ?? ""}
                  items={envList.map((e) => ({ value: e.id, label: e.name }))}
                  onChange={(v) => change(() => setEnvironment(v))}
                />
              )}
            </div>
          )}
          {env && (
            <p className="text-[12.5px] text-muted-foreground">
              Runs in {env.name}. Services in {env.name} can link to it.
            </p>
          )}
          {blockedEnv && (
            <Note tone="weld">
              {env.kind === "production"
                ? `Only a workspace admin can deploy models to ${env.name}. Ask one, or pick another environment.`
                : `You can't deploy models to ${env.name}. Ask a workspace admin for permission.`}
            </Note>
          )}
        </Section>

        <Section title={runsOn === "gpu" ? "GPU" : "Server"}>
          {options.length > 1 && (
            <Segmented
              label="Runs on"
              value={runsOn}
              items={[
                { value: "gpu", label: "GPU" },
                { value: "cpu", label: "CPU" },
              ]}
              onChange={(v) =>
                change(() => {
                  setRunsOn(v)
                  setTarget(undefined)
                  setQuant("")
                })
              }
            />
          )}
          {runsOn === "gpu" ? (
            target?.runsOn === "gpu" ? (
              <ChosenGpu
                target={target}
                onRegion={(region) =>
                  change(() => setTarget({ ...target, region }))
                }
                onChange={() => change(() => setTarget(undefined))}
              />
            ) : (
              <GpuTargets
                minMemoryGib={minMemory(offered, quant)}
                count={1}
                onSelect={(t) =>
                  change(() =>
                    setTarget({
                      runsOn: "gpu",
                      gpu: t,
                      region: t.regions?.[0],
                    })
                  )
                }
                refuse={(t) => refusal(offered, quant, t.accelerator.kind)}
              />
            )
          ) : (
            <CpuTargets
              value={target?.runsOn === "cpu" ? target.connectionId : ""}
              onChange={(connectionId, name) =>
                change(() => setTarget({ runsOn: "cpu", connectionId, name }))
              }
            />
          )}
        </Section>

        <Section title="Quantization">
          <div
            role="radiogroup"
            aria-label="Quantization"
            className="grid gap-1.5"
          >
            {offered.map((q) => {
              const fit = kind ? fitOn(q, kind) : undefined
              const off = !!kind && !!fit && !fitsStatus(fit)
              const checked = (quant || pv?.quantization) === q.id
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
                    onChange={() => change(() => setQuant(q.id))}
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
          {!quant && pv?.quantization && offered.length > 1 && (
            <p className="text-[12.5px] text-muted-foreground">
              {pv.quantization} is the best that fits. Pick another to change
              it.
            </p>
          )}
        </Section>

        <div className="grid gap-5 sm:grid-cols-2">
          <Section title="Context">
            <NumberField
              label="Context in tokens"
              value={context}
              placeholder={
                pv?.fit.need.maxModelLen
                  ? String(Math.min(pv.fit.need.maxModelLen, 32768))
                  : "The most that fits"
              }
              max={pv?.fit.need.maxModelLen || undefined}
              onChange={(v) => change(() => setContext(v))}
            />
            {pv?.fit.need.maxModelLen ? (
              <p className="text-[12.5px] text-muted-foreground">
                Up to {pv.fit.need.maxModelLen.toLocaleString("en-US")} tokens
                fit on {pv.gpu ? gpuTitle(pv.gpu.kind) : "CPU"}.
              </p>
            ) : null}
          </Section>
          <Section title="Budget alert">
            <NumberField
              label="Monthly budget in US dollars"
              value={budget}
              placeholder="Optional"
              prefix="$"
              decimal
              onChange={(v) => change(() => setBudget(v))}
            />
            <p className="text-[12.5px] text-muted-foreground">
              We&apos;ll tell admins before it passes this. We never stop it.
            </p>
          </Section>
        </div>

        {size && (
          <ServerReview
            admin={cloudAdmin}
            preview={pv}
            fresh={fresh}
            reviewed={reviewed}
            reviewing={reviewing}
            error={reviewError}
            onReview={() => void reviewServer()}
          />
        )}

        <Summary
          preview={shown}
          stale={!size && !!pv && !fresh}
          working={!!key && !fresh}
          error={result?.key === key ? result?.error : undefined}
          capacity={capacity}
          deployError={error}
        />
      </div>

      <DialogFooter>
        <Button variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button disabled={!canDeploy} onClick={() => void deploy()}>
          {deployMutation.isPending && <Spinner />}
          {size ? "Create the server and plan it" : "Plan it"}
        </Button>
      </DialogFooter>
    </>
  )
}

/** targetChoice is the deploy body's target for what was chosen. */
function targetChoice(t: Target): DeployInputBody["target"] {
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
function minMemory(offered: QuantizationView[], quant: string) {
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
function refusal(
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

function Section({ title, children }: { title: string; children: ReactNode }) {
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

function Choice({
  label,
  value,
  items,
  onChange,
}: {
  label: string
  value: string
  items: { value: string; label: string }[]
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
          <SelectItem key={i.value} value={i.value}>
            {i.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function Segmented<T extends string>({
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

function NumberField({
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

function Note({
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

function ChosenGpu({
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

function CpuTargets({
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

function ServerReview({
  admin,
  preview,
  fresh,
  reviewed,
  reviewing,
  error,
  onReview,
}: {
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
        <ReviewedServer server={server} />
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

function ReviewedServer({ server }: { server: Operation }) {
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
        Infrastructure, whether or not the model gets traffic. Deploy within
        five minutes of this review, or review it again.
      </p>
    </div>
  )
}

function Summary({
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
  deployError: string
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
      {pv?.environments[0]?.message && (
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
      {deployError && <Note tone="failed">{deployError}</Note>}
    </div>
  )
}

function ProblemLine({ problem }: { problem: Problem }) {
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

function CapacityLink({
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
function said(err: unknown, fallback: string): string {
  const raw = err instanceof Error ? err.message.trim() : ""
  if (!raw || /^HTTP \d+/.test(raw) || statusOf(err) >= 500) return fallback
  return /[.!?]$/.test(raw) ? raw : `${raw}.`
}
