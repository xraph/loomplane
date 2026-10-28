// Deploying a model, in one dialog: where it runs, on which GPU or server,
// how big, and what it costs. Every change asks the server for a fresh
// preview, which writes nothing. A GPU server Loomplane would create for
// the model gets its own "Review server" step, for admins only: that review
// asks the provider and records it in Infrastructure, so it's sent from
// that button and never on a change of the form.
//
// From a deployed model's page it adds an environment to that model: the
// environments it runs in already can't be picked, and the new one gets its
// own copy, on its own server, at its own cost.
import { useEffect, useRef, useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useAiModelDeploymentsList,
  useAiModelsDeploy,
  useAiModelsEntry,
  useAiModelsPreview,
  useEnvironmentsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  DeployInputBody,
  DeployPreview,
  DeployResult,
  Environment,
  EntryDetail,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import {
  CapacityLink,
  Choice,
  NumberField,
  Note,
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
import { listOf, runsOnOf, type RunsOn } from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"

type Props = {
  entryId: string
  /** The entry itself, when the caller has it, such as a pasted repo. */
  detail?: EntryDetail
  quantization?: string
  projectId?: string
  environmentId?: string
  /**
   * The deployed model this adds an environment to, sent as the preview's
   * and the deploy's resource.
   */
  resource?: string
  /** Environment IDs the model already runs in: they can't be picked. */
  runsIn?: string[]
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

function DeployForm({
  entry,
  quantization,
  projectId,
  environmentId,
  resource,
  runsIn = [],
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
  // An environment the model runs in already is never the one deployed to.
  const choosable = envList.filter((e) => !runsIn.includes(e.id))
  const env =
    choosable.find((e) => e.id === environment) ??
    choosable.find(mayWrite) ??
    choosable[0]

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
    ...(resource ? { resource } : {}),
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
  const [error, setError] = useState<DeployError>()

  const change = (f: () => void) => {
    f()
    setError(undefined)
    setReviewError("")
    setCapacity(undefined)
  }

  // A second click before the first request's state shows would send a
  // second request, and for a review a second record in Infrastructure.
  const inFlight = useRef({ review: false, deploy: false })

  async function reviewServer() {
    if (!body || !path || reviewing || inFlight.current.review) return
    inFlight.current.review = true
    setReviewing(true)
    setReviewError("")
    try {
      const r = await previewMutation.mutateAsync({
        path,
        body: { ...body, reviewServer: true },
      })
      setReview({ key, pv: r as DeployPreview })
      // What the last deploy said was about the review this one replaces.
      setError(undefined)
      setCapacity(undefined)
    } catch (err) {
      setReviewError(said(err, "Loomplane couldn't review the GPU server."))
    } finally {
      inFlight.current.review = false
      setReviewing(false)
    }
  }

  // openModel goes to the page of the model just deployed, or to the
  // Models page's Deployed tab when its name isn't known.
  function openModel(resource?: string) {
    if (path && resource)
      void navigate({
        to: "/projects/$id/ai/models/$resource",
        params: { id: path.projectId, resource },
        search: { environment: path.environmentId },
      })
    else void navigate({ to: "/ai/models", search: { tab: "deployed" } })
  }

  async function deploy() {
    if (!body || !path || deployMutation.isPending || inFlight.current.deploy)
      return
    const server = size ? reviewed?.server : undefined
    if (size && !server) return
    inFlight.current.deploy = true
    setError(undefined)
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
          resource: res.deployment.resource || (reviewed ?? pv)?.resource,
        })
        return
      }
      onPlanned?.(undefined)
      // A model waiting for its server: its page says how it's doing.
      openModel(res.deployment?.resource || (reviewed ?? pv)?.resource)
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
        // The review expired. The control that makes a new one is Review
        // server, whatever the server's sentence calls it.
        setReview(undefined)
        setError({
          message: said(err, "The GPU server's review has expired."),
          next: "Review the server again.",
        })
        return
      }
      const status = statusOf(err)
      if (server && (status === 0 || status >= 500)) {
        // The server's approval went with the request, so it may have been
        // recorded before the failure. Deploying again could create a
        // second server.
        setError({
          message:
            "Loomplane couldn't confirm the deploy. The GPU server may already be approved and on its way.",
          next: "Check the model page before you deploy again.",
          model: reviewed?.resource
            ? { ...path, resource: reviewed.resource }
            : undefined,
        })
        return
      }
      setError({ message: said(err, "The model wasn't deployed.") })
    } finally {
      inFlight.current.deploy = false
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
              openModel(capacity.resource)
            }}
          >
            Open the model page
          </Button>
        </DialogFooter>
      </>
    )

  const blockedEnv = env && !mayWrite(env)
  // Another environment the caller may deploy to, to point at instead.
  const elsewhere = choosable.some((e) => e.id !== env?.id && mayWrite(e))
  // What this deploy adds, once the preview for this environment says.
  const adds =
    pv?.addsTo && pv.environments[0]?.environmentId === env?.id ? pv : undefined
  const others = adds
    ? adds.environments
        .filter((e) => e.environmentId !== env?.id)
        .map((e) => e.environment)
    : []
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
              Create a project first. A model runs in its environments.
            </p>
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              <Choice
                label="Project"
                value={chosenProject}
                items={projectList
                  // Adding an environment stays in the model's project.
                  .filter((p) => !resource || p.id === projectId)
                  .map((p) => ({ value: p.id, label: p.name }))}
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
                  items={envList.map((e) =>
                    runsIn.includes(e.id)
                      ? {
                          value: e.id,
                          label: `${e.name} (runs there)`,
                          disabled: true,
                        }
                      : { value: e.id, label: e.name }
                  )}
                  onChange={(v) => change(() => setEnvironment(v))}
                />
              )}
            </div>
          )}
          {env && adds ? (
            <p className="text-[12.5px] text-pretty text-muted-foreground">
              Adds {env.name} to {adds.addsTo}.
              {others.length > 0 && ` It keeps running in ${listOf(others)}.`}{" "}
              Each environment runs its own copy, on its own server, at its own
              cost.
            </p>
          ) : env ? (
            <p className="text-[12.5px] text-muted-foreground">
              Runs in {env.name}. Services in {env.name} can link to it.
            </p>
          ) : envState?.data && runsIn.length > 0 ? (
            <p className="text-[12.5px] text-pretty text-muted-foreground">
              {resource ?? "This model"} runs in every environment of this
              project already.
            </p>
          ) : null}
          {blockedEnv && (
            <Note tone="weld">
              {env.kind === "production"
                ? `Only a workspace admin can deploy models to ${env.name}. ${elsewhere ? "Ask one, or pick another environment." : "Ask one."}`
                : `You can't deploy models to ${env.name}. Ask a workspace admin for permission.`}
            </Note>
          )}
        </Section>

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
