// Moving a model, with the deploy dialog's rules: every change asks the
// server for a fresh move preview, which writes nothing, and a GPU server
// Loomplane would create gets its own "Review server" step for admins.
import { useEffect, useRef, useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useAiModelDeploymentsMove,
  useAiModelDeploymentsMovePreview,
  useAiModelsEntry,
} from "@/client/forge/loomplane/hooks"
import type {
  DeployPreview,
  DeployResult,
  EntryDetail,
  ModelsMoveInputBody,
  ModelsStatus,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import {
  CapacityLink,
  NumberField,
  Placement,
  QuantizationChoice,
  said,
  Section,
  ServerReview,
  Summary,
  targetChoice,
  type CapacityOutcome,
  type DeployError,
  type Target,
} from "@/components/ai/models/deploy-parts"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { gpuTitle } from "@/lib/gpu"
import { runsOnOf, type RunsOn } from "@/lib/models"
import { useOrganization } from "@/lib/organization"

type Props = {
  status: ModelsStatus
  projectId: string
  environmentId: string
  onClose: () => void
  /** Called once the move went through, with what the server said. */
  onMoved: (result: DeployResult) => void
}

export function MoveModelDialog(props: Props) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
    >
      <DialogContent className="gap-5 rounded-2xl sm:max-w-[680px]">
        <DialogHeader className="gap-1 pr-6">
          <DialogTitle className="text-[17px] leading-normal font-semibold tracking-[-0.015em]">
            Move {props.status.resource}
          </DialogTitle>
          <DialogDescription className="leading-normal">
            It keeps running where it is until the move is deployed. Nothing
            changes until you plan it.
          </DialogDescription>
        </DialogHeader>
        {props.status.entry.id ? (
          <EntryLoader {...props} />
        ) : (
          <p className="text-[13px] text-muted-foreground">
            Loomplane doesn&apos;t know which registry model this is, so it
            can&apos;t move it. Edit its target in the manifest instead.
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}

function EntryLoader(props: Props) {
  const entry = useQuery(useAiModelsEntry, {
    query: { id: props.status.entry.id },
  })
  if (entry.status === "error")
    return (
      <QueryError
        error={entry.error}
        fallback="The model's registry entry couldn't be loaded."
        retry={entry.refetch}
      />
    )
  if (!entry.data)
    return (
      <div data-testid="move-loading" className="grid gap-3">
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-32 w-full rounded-xl" />
      </div>
    )
  return <MoveForm {...props} entry={entry.data} />
}

function MoveForm({
  status,
  projectId,
  environmentId,
  onClose,
  onMoved,
  entry,
}: Props & { entry: EntryDetail }) {
  const { can } = useOrganization()
  const previewMutation = useMutation(useAiModelDeploymentsMovePreview)
  const moveMutation = useMutation(useAiModelDeploymentsMove)
  const cloudAdmin = can("organization_cloud:manage")
  const path = { projectId, environmentId, resource: status.resource }

  const options = [
    ...new Set(entry.quantizations.flatMap((q) => runsOnOf(q.engine))),
  ].sort() as RunsOn[]
  const [runsOn, setRunsOn] = useState<RunsOn>(
    status.gpu && options.includes("gpu")
      ? "gpu"
      : options.includes("cpu")
        ? "cpu"
        : "gpu"
  )
  const offered = entry.quantizations.filter((q) =>
    runsOnOf(q.engine).includes(runsOn)
  )
  const [quant, setQuant] = useState("")
  const [target, setTarget] = useState<Target>()
  const [context, setContext] = useState("")

  const kind = target
    ? target.runsOn === "gpu"
      ? target.gpu.accelerator.kind
      : "cpu"
    : undefined
  const maxModelLen = Number.parseInt(context, 10)
  const body: ModelsMoveInputBody | undefined = target && {
    target: targetChoice(target),
    ...(quant ? { quantization: quant } : {}),
    ...(maxModelLen > 0 ? { maxModelLen } : {}),
  }
  const key = body ? JSON.stringify(body) : ""

  // The move preview of exactly this form, 300 ms after the last change.
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
    const body = JSON.parse(key) as ModelsMoveInputBody
    let current = true
    const timer = window.setTimeout(() => {
      preview
        .current({
          path: { projectId, environmentId, resource: status.resource },
          body,
        })
        .then((pv) => current && setResult({ key, pv: pv as DeployPreview }))
        .catch(
          (err: unknown) =>
            current &&
            setResult({
              key,
              error: said(err, "Loomplane couldn't work this move out."),
            })
        )
    }, 300)
    return () => {
      current = false
      window.clearTimeout(timer)
    }
  }, [key, projectId, environmentId, status.resource])
  const fresh = !!key && result?.key === key
  const pv = key ? result?.pv : undefined

  // The GPU server's review, for exactly this form.
  const [review, setReview] = useState<{ key: string; pv: DeployPreview }>()
  const [reviewing, setReviewing] = useState(false)
  const [reviewError, setReviewError] = useState("")
  const reviewed = review && review.key === key ? review.pv : undefined
  const size = target?.runsOn === "gpu" && target.gpu.source === "server_size"

  const [capacity, setCapacity] = useState<CapacityOutcome>()
  const [error, setError] = useState<DeployError>()
  // A move the capacity policy paused, said in the dialog first.
  const [paused, setPaused] = useState<DeployResult>()

  const change = (f: () => void) => {
    f()
    setError(undefined)
    setReviewError("")
    setCapacity(undefined)
  }

  // One request per click, however fast the clicks.
  const inFlight = useRef({ review: false, move: false })

  async function reviewServer() {
    if (!body || reviewing || inFlight.current.review) return
    inFlight.current.review = true
    setReviewing(true)
    setReviewError("")
    try {
      const r = await previewMutation.mutateAsync({
        path,
        body: { ...body, reviewServer: true },
      })
      setReview({ key, pv: r as DeployPreview })
      setError(undefined)
      setCapacity(undefined)
    } catch (err) {
      setReviewError(said(err, "Loomplane couldn't review the GPU server."))
    } finally {
      inFlight.current.review = false
      setReviewing(false)
    }
  }

  async function move() {
    if (!body || moveMutation.isPending || inFlight.current.move) return
    const server = size ? reviewed?.server : undefined
    if (size && !server) return
    inFlight.current.move = true
    setError(undefined)
    setCapacity(undefined)
    try {
      const res = (await moveMutation.mutateAsync({
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
      if (server && res.deployment?.message && !res.planId) {
        // Approved, and held back by the capacity policy for now.
        setCapacity({
          message: res.deployment.message,
          cloudId: server.cloudId,
          paused: true,
          resource: status.resource,
        })
        setPaused(res)
        return
      }
      onMoved(res)
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
      if (code === "model-server-review") {
        setReview(undefined)
        setError({
          message: said(err, "The GPU server's review has expired."),
          next: "Review the server again.",
        })
        return
      }
      const st = statusOf(err)
      if (server && (st === 0 || st >= 500)) {
        // The approval went with the request and may have been recorded.
        setError({
          message:
            "Loomplane couldn't confirm the move. The GPU server may already be approved and on its way.",
          next: "Check this page again in a minute before you move it again.",
        })
        return
      }
      setError({ message: said(err, "The model wasn't moved.") })
    } finally {
      inFlight.current.move = false
    }
  }

  if (capacity?.paused)
    return (
      <>
        <div className="grid gap-3">
          <StatusPill tone="waiting">Waiting for a server</StatusPill>
          <p className="text-[13.5px] text-pretty">{capacity.message}</p>
          <p className="text-[13px] text-muted-foreground">
            The move goes ahead once the server is up, and the model keeps
            running where it is until then. The server bills from then until you
            delete it in Infrastructure.
          </p>
          <CapacityLink cloudId={capacity.cloudId} />
        </div>
        <DialogFooter>
          <Button onClick={() => onMoved(paused ?? {})}>Close</Button>
        </DialogFooter>
      </>
    )

  const shown = size ? (reviewed ?? pv) : pv
  const ready = size
    ? !!reviewed?.server && reviewed.canDeploy
    : fresh && !!pv?.canDeploy
  const canMove = ready && !moveMutation.isPending

  return (
    <>
      <div className="grid min-w-0 gap-5">
        <Placement
          options={options}
          runsOn={runsOn}
          target={target}
          offered={offered}
          quant={quant}
          onRunsOn={(v) =>
            change(() => {
              setRunsOn(v)
              setTarget(undefined)
              setQuant("")
            })
          }
          onTarget={(t) => change(() => setTarget(t))}
        />
        <QuantizationChoice
          offered={offered}
          kind={kind}
          quant={quant}
          picked={pv?.quantization}
          onChange={(id) => change(() => setQuant(id))}
        />
        <Section title="Context">
          <NumberField
            label="Context in tokens"
            value={context}
            placeholder={
              status.maxModelLen > 0
                ? `Now ${status.maxModelLen.toLocaleString("en-US")}`
                : "The most that fits"
            }
            max={pv?.fit.need.maxModelLen || undefined}
            onChange={(v) => change(() => setContext(v))}
          />
          {pv?.fit.need.maxModelLen ? (
            <p className="text-[12.5px] text-muted-foreground">
              Up to {pv.fit.need.maxModelLen.toLocaleString("en-US")} tokens fit
              on {pv.gpu ? gpuTitle(pv.gpu.kind) : "CPU"}.
            </p>
          ) : null}
        </Section>
        {size && (
          <ServerReview
            admin={cloudAdmin}
            preview={pv}
            fresh={fresh}
            reviewed={reviewed}
            reviewing={reviewing}
            error={reviewError}
            action="Plan the move"
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
        <Button disabled={!canMove} onClick={() => void move()}>
          {moveMutation.isPending && <Spinner />}
          {size ? "Create the server and plan the move" : "Plan the move"}
        </Button>
      </DialogFooter>
    </>
  )
}
