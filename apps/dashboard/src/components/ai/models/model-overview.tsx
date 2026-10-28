// A deployed model at a glance: its state, what it runs on, what it costs
// and which services link to it.
import { useState, type ReactNode } from "react"

import {
  useAiModelDeploymentsCancel,
  useAiModelDeploymentsGet,
  useAiModelDeploymentsList,
  useAiModelDeploymentsMoveToPin,
  useAiModelDeploymentsRetry,
  useAiModelDeploymentsUpdate,
  useManifestsCurrent,
  useManifestsEdit,
} from "@/client/forge/loomplane/hooks"
import type { DeployResult, ModelsStatus } from "@/client/forge/loomplane/types"
import { statusOf } from "@/client/errors"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { said } from "@/components/ai/models/deploy-parts"
import { VerificationBadge } from "@/components/ai/models/verification-badge"
import { StatusPill } from "@/components/runs"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { acceleratorLabel } from "@/lib/gpu"
import {
  costLabel,
  dollars,
  engineName,
  engineOfKind,
  listOf,
  modelState,
  monthLabel,
  shortPin,
  wholeDollars,
} from "@/lib/models"
import { unlinkedServices } from "@/lib/fabriq"
import { useOrganization } from "@/lib/organization"
import { useOnce } from "@/lib/use-once"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

export type ModelProps = {
  status: ModelsStatus
  projectId: string
  environmentId: string
  /** Production's models need ai:manage to change. */
  production: boolean
  /** Shows what a change said at the top of the page. */
  notify: (notice: ModelNotice | undefined) => void
  onTab: (tab: "overview" | "example" | "settings") => void
}

/**
 * ModelNotice is what a change said, shown at the top of the page so it
 * stays when the model is gone, such as after a cancel or a removal. A
 * weld one is about money: a server that keeps billing.
 */
export type ModelNotice = {
  title?: string
  text: string
  tone?: "weld"
  planId?: string
  /** Links Infrastructure, where a GPU server is deleted. */
  infrastructure?: boolean
}

/** noEdit says why someone can't change the model, and who can. */
export function noEdit(status: ModelsStatus, production: boolean): string {
  const env = status.environments[0]?.environment || "this environment"
  return production
    ? `Only a workspace admin can change models in ${env}.`
    : `You can't change models in ${env}. Ask a workspace admin for permission.`
}

/** costsMoney reports whether a sentence is about a server that bills. */
export function costsMoney(text: string): boolean {
  return /costs money/.test(text)
}

export function ModelOverview(props: ModelProps) {
  return (
    <div className="grid min-w-0 gap-6">
      <StateLine {...props} />
      {props.status.updateAvailable && <UpdateAvailable {...props} />}
      <Facts status={props.status} />
      <CostCard {...props} />
      <LinkedServices {...props} />
    </div>
  )
}

/* ------------------------------------------------------------------------ */

function Plan({ id }: { id?: string }) {
  if (!id) return null
  return (
    <Link
      to="/deployments/$id"
      params={{ id }}
      className="w-fit text-[13px] font-medium text-primary hover:underline"
    >
      Open the plan
    </Link>
  )
}

// The sentence the server adds to a failed change's hint while the version
// before it still serves. The state line says it once, in its own words.
const stillServesHint =
  "The version deployed before is still running, so linked services keep working while you fix this."

function StateLine({
  status,
  projectId,
  environmentId,
  production,
  notify,
  onTab,
}: ModelProps) {
  const invalidate = useInvalidate()
  const retry = useMutation(useAiModelDeploymentsRetry)
  const cancel = useMutation(useAiModelDeploymentsCancel)
  const [refused, setRefused] = useState("")
  const path = { projectId, environmentId, resource: status.resource }
  // The version before a failed change is still bound, so it still serves.
  const stillServes = !!status.internalUrl
  const state = modelState(status.state, stillServes)
  const busy = retry.isPending || cancel.isPending
  const once = useOnce()
  const why = status.canEdit ? undefined : noEdit(status, production)

  const tryAgain = () => once("retry", tryAgainNow)

  async function tryAgainNow() {
    setRefused("")
    notify(undefined)
    try {
      const res = (await retry.mutateAsync({ path })) as ModelsStatus
      notify({
        text:
          res.state === "waiting_for_server"
            ? `${status.resource} waits for its GPU server again.`
            : `Loomplane planned ${status.resource} again.`,
        planId: res.planId,
      })
      invalidate(useAiModelDeploymentsGet)
      invalidate(useAiModelDeploymentsList)
    } catch (err) {
      setRefused(said(err, "Loomplane couldn't try it again."))
    }
  }

  const stopWaiting = () => once("cancel", stopWaitingNow)

  async function stopWaitingNow() {
    setRefused("")
    notify(undefined)
    try {
      const res = (await cancel.mutateAsync({ path })) as { message?: string }
      const text = res.message?.trim()
      // The server's sentence says when the server was already approved,
      // and that it bills until someone deletes it.
      notify(
        text
          ? {
              title: "Canceled",
              text,
              tone: costsMoney(text) ? "weld" : undefined,
              infrastructure: true,
            }
          : {
              title: "Canceled",
              text: `${status.resource} no longer waits for a GPU server, and Loomplane didn't create one.`,
            }
      )
      invalidate(useAiModelDeploymentsGet)
      invalidate(useAiModelDeploymentsList)
    } catch (err) {
      setRefused(said(err, "Loomplane couldn't cancel it."))
    }
  }

  const waiting = status.state === "waiting_for_server"
  const attention = status.state === "needs_attention"
  const failed = status.state === "failed"
  const hint = (status.hint ?? "").replace(stillServesHint, "").trim()
  // The server's hint after the engine ran out of GPU memory.
  const memory = /didn't fit in GPU memory/.test(hint)
  return (
    <section
      aria-label="State"
      className="grid min-w-0 gap-2 rounded-xl p-4 ring-1 ring-border"
    >
      <div className="flex flex-wrap items-center gap-2">
        <StatusPill tone={state.tone}>{state.label}</StatusPill>
        {status.state === "planned" && <Plan id={status.planId} />}
      </div>
      {status.state === "deploying" && (
        <p className="text-[13.5px]">
          Downloading and loading weights. Big models take a while.
        </p>
      )}
      {status.state === "planned" && (
        <p className="text-[13.5px]">
          The plan that deploys it is waiting for approval.
        </p>
      )}
      {failed && stillServes && (
        <p className="text-[13.5px] font-medium">
          The change didn&apos;t start. The version deployed before is still
          running, so linked services keep working while you fix this.
        </p>
      )}
      {(waiting || attention || failed) && status.message && (
        <p className="text-[13.5px] text-pretty">{status.message}</p>
      )}
      {waiting && !status.message && (
        <p className="text-[13.5px]">
          Waiting for its GPU server. It deploys once the server is up.
        </p>
      )}
      {failed && hint && (
        <p className="text-[13px] text-muted-foreground">{hint}</p>
      )}
      {failed && memory && (
        <Button
          size="sm"
          variant="outline"
          className="w-fit"
          onClick={() => onTab("settings")}
        >
          Move it with a shorter context
        </Button>
      )}
      {status.state === "ready" && status.internalUrl && (
        <p className="text-[13px] text-muted-foreground">
          Linked services reach it at{" "}
          <span className="font-mono text-[12.5px] break-all">
            {status.internalUrl}
          </span>
          .
        </p>
      )}
      {(waiting || attention || (failed && status.deployment)) && (
        <div className="flex flex-wrap items-center gap-2">
          {(attention || failed) && (
            <Button
              size="sm"
              variant="outline"
              disabled={!!why || busy}
              onClick={() => void tryAgain()}
            >
              {retry.isPending && <Spinner />}
              Retry
            </Button>
          )}
          {(waiting || attention) && (
            <Button
              size="sm"
              variant="outline"
              disabled={!!why || busy}
              onClick={() => void stopWaiting()}
            >
              {cancel.isPending && <Spinner />}
              Cancel
            </Button>
          )}
          {why && (
            <span className="text-[12.5px] text-muted-foreground">{why}</span>
          )}
        </div>
      )}
      {refused && (
        <p role="alert" className="text-[13px] text-destructive">
          {refused}
        </p>
      )}
    </section>
  )
}

function UpdateAvailable({
  status,
  projectId,
  environmentId,
  production,
}: ModelProps) {
  const invalidate = useInvalidate()
  const moveToPin = useMutation(useAiModelDeploymentsMoveToPin)
  const [confirming, setConfirming] = useState(false)
  const [result, setResult] = useState<{
    text: string
    planId?: string
    failed?: boolean
  }>()
  const pin = status.pin ? shortPin(status.pin) : "the new pin"
  const here = status.environments[0]?.environment || "its environment"
  // Each environment moves to the new pin from its own page.
  const others = status.environments.slice(1).map((e) => e.environment)
  const alone =
    others.length > 0
      ? `Moves ${here} only. ${listOf(others)} ${others.length > 1 ? "keep their" : "keeps its"} weights until you move ${others.length > 1 ? "them from their" : "it from its"} own page.`
      : undefined

  const once = useOnce()
  const go = () => once("move-to-pin", goNow)

  async function goNow() {
    setConfirming(false)
    setResult(undefined)
    try {
      const res = (await moveToPin.mutateAsync({
        path: { projectId, environmentId, resource: status.resource },
      })) as DeployResult
      setResult({
        text:
          res.message?.trim() ||
          `The move to ${pin} is planned. Nothing changes until you approve it.`,
        planId: res.planId,
      })
      invalidate(useAiModelDeploymentsGet)
      invalidate(useAiModelDeploymentsList)
    } catch (err) {
      setResult({
        text: said(err, "Loomplane couldn't plan the move."),
        failed: true,
      })
    }
  }

  return (
    <section
      aria-label="Update available"
      className="grid min-w-0 gap-2 rounded-xl bg-subtle p-4 ring-1 ring-border"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="grid min-w-0 flex-[1_1_320px] gap-0.5">
          <h3 className="text-[13px] font-semibold">Update available</h3>
          <p className="text-[13px] text-muted-foreground">
            {status.pin?.message ||
              "The registry now pins other weights for this model."}
          </p>
          {alone && (
            <p className="text-[13px] text-pretty text-muted-foreground">
              {alone}
            </p>
          )}
        </div>
        <Button
          size="sm"
          disabled={!status.canEdit || moveToPin.isPending}
          onClick={() => setConfirming(true)}
        >
          {moveToPin.isPending && <Spinner />}
          <span className="truncate">Move to {pin}</span>
        </Button>
      </div>
      {!status.canEdit && (
        <p className="text-[12.5px] text-muted-foreground">
          {noEdit(status, production)}
        </p>
      )}
      {result && (
        <div className="grid gap-1" role="status">
          <p className={cn("text-[13px]", result.failed && "text-destructive")}>
            {result.text}
          </p>
          <Plan id={result.planId} />
        </div>
      )}
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Move {status.resource} to {pin}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Loomplane points {status.resource} at the weights the registry
              pins now and plans {here}. It keeps serving the weights it has
              until you approve the plan and the new ones are loaded.
              {alone && ` ${alone}`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep what it has</AlertDialogCancel>
            <AlertDialogAction onClick={() => void go()}>
              Plan the move
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

/* ------------------------------------------------------------------------ */

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <dt className="text-[12px] text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-[13.5px] break-words">{children}</dd>
    </div>
  )
}

function Facts({ status }: { status: ModelsStatus }) {
  const gpu = status.gpu
    ? `${acceleratorLabel(status.gpu)}${status.connectionName ? ` on ${status.connectionName}` : ""}`
    : `CPU${status.connectionName ? ` on ${status.connectionName}` : ""}`
  const engine = engineOfKind(status.kind)
  const quantization = status.deployment?.quantization
  return (
    <dl className="grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-4">
      <Fact label="Runs on">{gpu}</Fact>
      <Fact label="Engine">
        {engine ? engineName(engine) : "Unknown"}
        {engine && status.engineVersion && (
          // The release Loomplane ships now, which isn't necessarily the one
          // this model was deployed with.
          <span className="block text-[12.5px] text-muted-foreground">
            Loomplane&apos;s current {engineName(engine)} release is{" "}
            {status.engineVersion}
          </span>
        )}
      </Fact>
      <Fact label="Quantization">{quantization || "Unknown"}</Fact>
      <Fact label="Context">
        {status.maxModelLen > 0
          ? `${status.maxModelLen.toLocaleString("en-US")} tokens`
          : "Unknown"}
      </Fact>
      <Fact label="Verification">
        <VerificationBadge
          status={status.verification.status}
          fit={status.verification}
        />
      </Fact>
    </dl>
  )
}

/* ------------------------------------------------------------------------ */

function CostCard({
  status,
  projectId,
  environmentId,
  production,
}: ModelProps) {
  const invalidate = useInvalidate()
  const update = useMutation(useAiModelDeploymentsUpdate)
  const row = status.deployment
  const label = costLabel(status.cost, status.connectionName)
  const cpu = label.kind === "cpu"
  const projected = wholeDollars(status.projectedUsd)

  // The fields start from the row as read, and start again from it when
  // its revision changes: a refetch, or someone else's save.
  const [budget, setBudget] = useState(row?.monthlyBudgetUsd ?? "")
  const [hourly, setHourly] = useState(row?.hourlyUsdOverride ?? "")
  const [revision, setRevision] = useState(row?.revision)
  if (row?.revision !== revision) {
    setRevision(row?.revision)
    setBudget(row?.monthlyBudgetUsd ?? "")
    setHourly(row?.hourlyUsdOverride ?? "")
  }
  const [saved, setSaved] = useState<{ text: string; failed?: boolean }>()

  async function save(body: {
    monthlyBudgetUsd?: string
    hourlyUsdOverride?: string
  }) {
    if (!row) return
    setSaved(undefined)
    try {
      await update.mutateAsync({
        path: { projectId, environmentId, resource: status.resource },
        body: { ...body, revision: row.revision },
      })
      setSaved({ text: "Saved." })
      invalidate(useAiModelDeploymentsGet)
      invalidate(useAiModelDeploymentsList)
    } catch (err) {
      const stale = statusOf(err) === 409
      setSaved({
        text: stale
          ? "Someone else changed this model. Its page has the latest, so check it and save again."
          : said(err, "It wasn't saved."),
        failed: true,
      })
      if (stale) invalidate(useAiModelDeploymentsGet)
    }
  }

  const handmade = !row
    ? "This model was added to the manifest by hand, so Loomplane keeps no budget or price for it."
    : undefined
  // Whoever may change the model sets its price and its budget alert:
  // ai:edit, or ai:manage in production, as the server checks.
  const why =
    handmade ?? (status.canEdit ? undefined : noEdit(status, production))
  const unknown = label.kind === "unknown"
  const month = monthLabel(status)

  return (
    <section
      aria-label="Cost"
      className="grid min-w-0 gap-4 rounded-xl p-4 ring-1 ring-border"
    >
      <h3 className="text-[13px] font-semibold">Cost</h3>
      {cpu ? (
        // Never $0.00: a CPU model's cost is its server's.
        <p className="text-[13.5px]" data-testid="model-cost">
          {label.detail}
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-3" data-testid="model-cost">
          <CostFigure
            label="Per hour"
            value={unknown ? "Price unknown" : label.hourly}
            quiet={unknown}
            big
            tag={label.kind === "estimate" ? "estimate" : undefined}
            note={
              unknown
                ? "Loomplane doesn't know what this GPU costs. Set it below and the projection and budget alert work."
                : label.kind === "estimate"
                  ? "List price, estimate. Not a bill."
                  : label.detail
            }
          />
          <CostFigure
            label="This month"
            value={
              unknown
                ? "unknown"
                : Number(status.monthToDateUsd) > 0
                  ? `${month} so far`
                  : "Nothing yet"
            }
            quiet={unknown}
          />
          <CostFigure
            label="Projected"
            value={projected ? `${projected} projected this month` : "unknown"}
            quiet={!projected}
            tag={
              projected && label.kind === "estimate" ? "estimate" : undefined
            }
          />
        </div>
      )}
      {status.moveCost && <MoveCost status={status} />}
      {!cpu && (
        <div className="grid gap-4 border-t pt-4 sm:grid-cols-2">
          <div className="grid min-w-0 content-start gap-1.5">
            <label htmlFor="model-hourly" className="text-[12.5px] font-medium">
              It costs me per hour
            </label>
            <FieldRow
              id="model-hourly"
              prefix="$"
              value={hourly}
              placeholder={unknown ? "For example 1.20" : "Optional"}
              disabled={!!why}
              onChange={setHourly}
              onSave={() => void save({ hourlyUsdOverride: hourly.trim() })}
              busy={update.isPending}
              action="Save price"
            />
            <p className="text-[12.5px] text-muted-foreground">
              For a GPU Loomplane can&apos;t price, or one you pay a different
              price for. Clear it to go back to the list price.
            </p>
          </div>
          <div className="grid min-w-0 content-start gap-1.5">
            <label htmlFor="model-budget" className="text-[12.5px] font-medium">
              Budget alert per month
            </label>
            <FieldRow
              id="model-budget"
              prefix="$"
              value={budget}
              placeholder="Optional"
              disabled={!!why}
              onChange={setBudget}
              onSave={() => void save({ monthlyBudgetUsd: budget.trim() })}
              busy={update.isPending}
              action="Save budget"
            />
            <p className="text-[12.5px] text-muted-foreground">
              We&apos;ll tell admins before it passes this. We never stop it.
            </p>
          </div>
          {why && (
            <p
              className={cn(
                "text-[12.5px] sm:col-span-2",
                handmade ? "text-muted-foreground" : "text-weld-ink"
              )}
            >
              {why}
            </p>
          )}
          {saved && (
            <p
              role={saved.failed ? "alert" : "status"}
              className={cn(
                "text-[13px] sm:col-span-2",
                saved.failed ? "text-destructive" : "text-muted-foreground"
              )}
            >
              {saved.text}
            </p>
          )}
        </div>
      )}
    </section>
  )
}

function CostFigure({
  label,
  value,
  quiet,
  big,
  tag,
  note,
}: {
  label: string
  value: string
  quiet?: boolean
  big?: boolean
  tag?: string
  note?: string
}) {
  return (
    <div className="grid min-w-0 content-start gap-0.5">
      <span className="text-[12px] text-muted-foreground">{label}</span>
      <span
        className={cn(
          "font-semibold tracking-tight",
          big ? "text-[20px]" : "text-[15px]",
          quiet ? "text-muted-foreground" : "tabular-nums"
        )}
      >
        {value}
        {tag && (
          <span className="ml-1.5 text-[12px] font-normal text-muted-foreground">
            {tag}
          </span>
        )}
      </span>
      {note && (
        <span className="text-[12.5px] text-pretty text-muted-foreground">
          {note}
        </span>
      )}
    </div>
  )
}

/**
 * MoveCost is what the GPU server a move waits for costs. The model keeps
 * running where it is until the move is deployed, so for a while both
 * bill.
 */
function MoveCost({ status }: { status: ModelsStatus }) {
  const c = status.moveCost
  if (!c) return null
  const hourly =
    c.known && c.source !== "cpu" ? dollars(c.hourlyUsd) : undefined
  return (
    <div
      data-testid="move-cost"
      className="grid gap-1 rounded-lg bg-weld-tint p-3 text-[13px] text-weld-ink"
    >
      <p className="font-medium">
        {hourly
          ? `Moving: the new GPU server costs ${hourly}/hr${c.estimate ? " (estimate)" : ""}.`
          : "Moving: what the new GPU server costs is unknown."}
      </p>
      <p className="text-pretty">
        The model keeps running here until the move is deployed, so both bill
        once the new server is up. A server Loomplane created for it here keeps
        billing after that, until you delete it in Infrastructure.
      </p>
    </div>
  )
}

function FieldRow({
  id,
  prefix,
  value,
  placeholder,
  disabled,
  busy,
  action,
  onChange,
  onSave,
}: {
  id: string
  prefix: string
  value: string
  placeholder: string
  disabled: boolean
  busy: boolean
  action: string
  onChange: (v: string) => void
  onSave: () => void
}) {
  return (
    <div className="flex min-w-0 gap-2">
      <div className="flex h-9 min-w-0 flex-1 items-center gap-1.5 rounded-[9px] border border-input bg-background px-2.5 focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/40">
        <span aria-hidden="true" className="text-muted-foreground">
          {prefix}
        </span>
        <input
          id={id}
          type="text"
          inputMode="decimal"
          value={value}
          placeholder={placeholder}
          disabled={disabled}
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
          className="h-full w-full min-w-0 bg-transparent font-mono text-[13px] tabular-nums outline-none placeholder:font-sans placeholder:text-muted-foreground"
        />
      </div>
      <Button
        size="sm"
        variant="outline"
        className="h-9"
        disabled={disabled || busy}
        onClick={onSave}
      >
        {busy && <Spinner />}
        {action}
      </Button>
    </div>
  )
}

/* ------------------------------------------------------------------------ */

function LinkedServices({ status, projectId }: ModelProps) {
  const { can } = useOrganization()
  // Each environment runs its own copy, so this environment's services are
  // the ones that link to this copy.
  const home = status.environments[0]?.environment ?? ""
  const reachable = status.linked.filter((l) => l.environment === home || !home)
  return (
    <section aria-label="Linked services" className="grid min-w-0 gap-3">
      <div className="grid gap-0.5">
        <h3 className="text-[13px] font-semibold">Linked services</h3>
        <p className="text-[12.5px] text-muted-foreground">
          {status.environments[0]?.message ||
            `Services in ${home} link to it and get MODEL_URL and MODEL_API_KEY.`}
        </p>
      </div>
      {reachable.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          No service links to this model yet.
        </p>
      ) : (
        <Card className="py-0">
          <ul aria-label="Linked services">
            {reachable.map((l) => (
              <li
                key={`${l.environment}/${l.service}`}
                className="grid gap-1 border-t px-4 py-3 first:border-t-0 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-4"
              >
                <div className="grid min-w-0 gap-0.5">
                  <span className="truncate font-mono text-[13px] font-medium">
                    {l.service}
                  </span>
                  <span className="text-[12px] text-muted-foreground">
                    {l.environment}
                  </span>
                </div>
                <div className="grid min-w-0 gap-1 sm:justify-items-end">
                  <StatusPill tone={l.reachable ? "live" : "failed"}>
                    {l.reachable ? "Reachable" : "Can't reach it"}
                  </StatusPill>
                  {!l.reachable && l.message && (
                    <span className="max-w-[48ch] text-[12.5px] text-pretty text-muted-foreground sm:text-right">
                      {l.message}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <LinkService
        projectId={projectId}
        resource={status.resource}
        environment={home}
        canEdit={can("project:update")}
      />
    </section>
  )
}

/**
 * LinkService makes a service of the project need the model, which gets it
 * MODEL_URL and MODEL_API_KEY on its next plan. It's the manifest's own
 * link edit, and plans nothing.
 */
function LinkService({
  projectId,
  resource,
  environment,
  canEdit,
}: {
  projectId: string
  resource: string
  environment: string
  canEdit: boolean
}) {
  const invalidate = useInvalidate()
  const manifest = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const edit = useMutation(useManifestsEdit)
  const [open, setOpen] = useState(false)
  const [service, setService] = useState("")
  const [outcome, setOutcome] = useState<{ text: string; failed?: boolean }>()

  const services = unlinkedServices(manifest.data?.manifest?.services, resource)
  const chosen = services.includes(service) ? service : (services[0] ?? "")

  async function link() {
    if (!chosen || edit.isPending) return
    setOutcome(undefined)
    try {
      await edit.mutateAsync({
        path: { id: projectId },
        body: { link: { service: chosen, need: resource } },
      })
      invalidate(useManifestsCurrent)
      invalidate(useAiModelDeploymentsGet)
      setOutcome({
        text: `${chosen} links to ${resource} now. It gets MODEL_URL and MODEL_API_KEY in ${environment} on its next release.`,
      })
      setOpen(false)
    } catch (err) {
      setOutcome({
        text:
          statusOf(err) === 403
            ? `You can't change this project's services. Ask a workspace admin to link ${chosen}.`
            : said(err, `${chosen} wasn't linked.`),
        failed: true,
      })
    }
  }

  return (
    <div className="grid gap-2">
      {!open ? (
        <Button
          size="sm"
          variant="outline"
          className="w-fit"
          disabled={!canEdit}
          onClick={() => setOpen(true)}
        >
          Link a service
        </Button>
      ) : (
        <div className="flex flex-wrap items-end gap-2.5 rounded-xl bg-subtle p-3 ring-1 ring-border">
          <div className="grid min-w-0 flex-[1_1_200px] gap-1.5">
            <span className="text-[12.5px] font-medium">Service</span>
            <Select
              items={services.map((s) => ({ label: s, value: s }))}
              value={chosen || null}
              onValueChange={(v) => v && setService(String(v))}
              disabled={services.length === 0}
            >
              <SelectTrigger
                aria-label="Service"
                className="h-9 w-full min-w-0"
              >
                <SelectValue placeholder="No service to link" />
              </SelectTrigger>
              <SelectContent>
                {services.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button
            disabled={!chosen || edit.isPending}
            onClick={() => void link()}
          >
            {edit.isPending && <Spinner />}
            Link service
          </Button>
          <Button variant="ghost" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          {services.length === 0 && (
            <p className="basis-full text-[12.5px] text-muted-foreground">
              Every service in the project already links to this model.
            </p>
          )}
        </div>
      )}
      {!canEdit && (
        <p className="text-[12.5px] text-muted-foreground">
          Linking a service changes the project&apos;s manifest. Ask someone who
          can edit the project.
        </p>
      )}
      {outcome && (
        <p
          role="status"
          className={cn(
            "text-[13px]",
            outcome.failed ? "text-destructive" : "text-muted-foreground"
          )}
        >
          {outcome.text}
        </p>
      )}
    </div>
  )
}
