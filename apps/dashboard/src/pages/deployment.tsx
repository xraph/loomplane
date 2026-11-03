// One deployment, start to finish: the plan, the decision on it, and each
// step it took. A run is named by its plan's ID, because a plan exists from
// the moment someone asks for one and a deployment only once it's approved.
import { LoaderCircleIcon } from "lucide-react"
import { type ReactNode, useMemo, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDeploymentsForPlan,
  useDeploymentsList,
  useDeploymentsRetry,
  usePlansCreate,
  usePlansGet,
  usePlansList,
  useRollbacksTarget,
} from "@/client/forge/loomplane/hooks"
import type {
  Action,
  Connection,
  Finding,
  Plan,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ApprovalCard } from "@/components/approval"
import { CancelDeployment } from "@/components/cancel-deployment"
import { BuildLogPanel, type LogSource } from "@/components/build-log-panel"
import { CanaryPanel } from "@/components/canary"
import { providerName } from "@/components/cloud-detail/clusters"
import {
  Cell,
  CloudCallout,
  CloudPageHeader,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { FindingActions } from "@/components/deploy"
import { DeploymentCapacityWait } from "@/components/deployment-capacity-wait"
import { FailedDeploymentCleanup } from "@/components/failed-deployment-cleanup"
import { DeploymentsIcon } from "@/components/icons"
import { Page } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { Artifacts, Leftovers, ReleaseKind } from "@/components/release-plan"
import { ReleaseWatchPanel } from "@/components/release-watch"
import { RollbackDialog } from "@/components/rollback-dialog"
import {
  Code,
  CopyButton,
  StatusPill,
  type Tone,
  toneClass,
  toneOf,
} from "@/components/runs"
import { ServiceAddress } from "@/components/service-address"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { DropdownMenuItem } from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import { commitURL, resourceLabel } from "@/lib/architecture"
import { showsCanary } from "@/lib/canary"
import { cloudLabel } from "@/lib/clouds"
import { duration, formatDate, relative } from "@/lib/format"
import { useProjectCan } from "@/lib/project-permissions"
import {
  opLabel,
  planConnections,
  planServices,
  planStatus,
  scopeBody,
} from "@/lib/plans"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import {
  isLive,
  normalizePlan,
  type Run,
  runKindLabel,
  runLog,
  runStatusWord,
  type ThreadStep,
} from "@/lib/runs"
import { useUrlTab } from "@/lib/use-url-tab"
import { useNow, usePeople, useRunFor } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** The deployment page's tabs, in order. */
export type DeploymentTab = "changes" | "logs" | "services" | "details"
export const deploymentTabs: DeploymentTab[] = [
  "changes",
  "logs",
  "services",
  "details",
]

export function DeploymentPage({
  id,
  tab,
}: {
  id: string
  /** The tab the page opens on; Changes while a plan waits, else Logs. */
  tab?: DeploymentTab
}) {
  const plan = useQuery(usePlansGet, { path: { id } })
  if (plan.data)
    return (
      <Loaded
        plan={normalizePlan(plan.data)}
        refetch={plan.refetch}
        tab={tab}
      />
    )
  const missing = plan.status === "error" && statusOf(plan.error) === 404
  return (
    <>
      <SiteHeader
        crumbs={[{ label: "Deployments", to: "/deployments" }, { label: id }]}
      />
      <Page>
        {plan.status === "error" ? (
          missing ? (
            <CloudEmpty
              kind="notice"
              icon={DeploymentsIcon}
              title="No deployment here by that ID"
              action={
                <Button
                  nativeButton={false}
                  render={<Link to="/deployments" />}
                >
                  All deployments
                </Button>
              }
            >
              It may belong to another workspace, or the link is wrong.
            </CloudEmpty>
          ) : (
            <QueryError
              error={plan.error}
              fallback="The deployment couldn't be loaded."
              retry={plan.refetch}
            />
          )
        ) : (
          <div className="grid gap-6" aria-busy="true">
            <Skeleton className="h-12 w-96" />
            <Skeleton className="h-16 rounded-xl" />
            <Skeleton className="h-72 rounded-xl" />
          </div>
        )}
      </Page>
    </>
  )
}

function Loaded({
  plan,
  refetch,
  tab: wanted,
}: {
  plan: Plan
  refetch: () => Promise<unknown>
  tab?: DeploymentTab
}) {
  const run = useRunFor(plan, refetch)
  const can = useProjectCan(plan.projectId)
  const now = useNow()
  const person = usePeople()
  const lines = useMemo(() => runLog(run, (u) => person(u).name), [run, person])
  const deciding = run.status === "waiting" || run.status === "blocked"
  const [tab, setTab] = useUrlTab<DeploymentTab>(
    wanted,
    deciding ? "changes" : "logs"
  )
  const [source, setSource] = useState("all")
  const live = isLive(run)
  const sources = logSources(run)
  const showLogs = (id: string) => {
    setSource(sources.some((s) => s.id === id) ? id : "all")
    setTab("logs")
  }
  const projectName = run.project?.name ?? "Project"

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Deployments", to: "/deployments" },
          {
            label: projectName,
            link: <Link to="/projects/$id" params={{ id: run.projectId }} />,
          },
          { label: run.environment },
        ]}
      />
      <Page>
        <Heading run={run} now={now} />
        <ProgressStrip
          run={run}
          now={now}
          selected={tab === "logs" ? source : null}
          onPick={showLogs}
        />
        {run.status === "waiting" && <ApprovalCard run={run} />}
        <FailureCallout run={run} />
        {run.status === "blocked" && <BlockedCallout run={run} />}
        {run.deployment && (
          <DeploymentCapacityWait deployment={run.deployment} />
        )}
        {((plan.catalog &&
          plan.actions.length === 1 &&
          plan.actions[0].provider === "cloud-sql" &&
          plan.actions[0].op === "create") ||
          plan.actions.some(
            (action) =>
              !!action.service?.clusterDemand || !!action.cleanupTargets?.length
          )) &&
          run.deployment &&
          ["failed", "cancelled"].includes(run.deployment.status) && (
            <FailedDeploymentCleanup
              id={run.deployment.id}
              canApprove={can("deployment:approve")}
              onChanged={refetch}
            />
          )}
        {showsCanary(run) && (
          <CanaryPanel
            deploymentId={run.deployment?.id}
            release={run.plan.release}
          />
        )}
        {run.deployment?.status === "succeeded" && (
          <ReleaseWatchPanel deploymentId={run.deployment.id} />
        )}
        <Tabs
          value={deploymentTabs.includes(tab) ? tab : "logs"}
          onValueChange={(v) => setTab(v as DeploymentTab)}
          className="min-w-0"
        >
          <TabsList
            variant="line"
            aria-label="Deployment views"
            className="mb-5 max-w-full overflow-x-auto"
          >
            <TabsTrigger value="changes">
              Changes
              <TabsCount>{run.plan.actions.length}</TabsCount>
            </TabsTrigger>
            <TabsTrigger value="logs">
              Logs
              {live && (
                <span
                  aria-hidden="true"
                  className="size-1.5 animate-pulse rounded-full bg-weld motion-reduce:animate-none"
                />
              )}
            </TabsTrigger>
            <TabsTrigger value="services">Services</TabsTrigger>
            <TabsTrigger value="details">Details</TabsTrigger>
          </TabsList>
          <TabsContent value="changes">
            <ChangesTab run={run} />
          </TabsContent>
          <TabsContent value="logs">
            <BuildLogPanel
              activity={lines}
              deploymentId={run.deployment?.id}
              live={live}
              name={run.deployment?.id ?? run.id}
              sources={sources}
              source={source}
              onSource={setSource}
            />
          </TabsContent>
          <TabsContent value="services">
            <ServicesTab run={run} onLogs={showLogs} />
          </TabsContent>
          <TabsContent value="details">
            <DetailsTab run={run} now={now} />
          </TabsContent>
        </Tabs>
      </Page>
    </>
  )
}

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

function Heading({ run, now }: { run: Run; now: number }) {
  const can = useProjectCan(run.projectId)
  const navigate = useNavigate()
  const person = usePeople()
  const replan = useMutation(usePlansCreate)
  const invalidate = useInvalidate()
  const author = person(run.createdBy)
  const style = run.project ? projectStyle(run.project) : null
  const d = run.deployment
  const p = run.plan
  const moving = p.sources.filter((s) => s.inScope)
  const services = planServices(p)
  const kind = runKindLabel(p)
  const rollback = mayOfferRollback(run, can("deployment:create"))
  const projectName = run.project?.name ?? "the project"

  const planAgain = async () => {
    try {
      const next = await replan.mutateAsync({
        path: { id: run.projectId },
        body: {
          environment: run.environment,
          // Planning again moves what this plan moved and holds what it held.
          ...scopeBody(
            p.sources.map((s) => s.alias),
            moving.map((s) => s.alias)
          ),
          ...(services.length > 0 ? { services } : {}),
        },
      })
      invalidate(usePlansList)
      void navigate({ to: "/deployments/$id", params: { id: next.id } })
      toast.success(`Planning ${run.environment} again`)
    } catch (err) {
      toast.error(messageOf(err, "The plan couldn't start."))
    }
  }

  const api = `curl -H "Authorization: Bearer $LOOMPLANE_API_KEY" ${window.location.origin}/api/v1/plans/${run.id}`
  const planAgainButton = !isLive(run) && can("deployment:create") && (
    <Button
      variant="outline"
      disabled={replan.isPending}
      onClick={() => void planAgain()}
    >
      {replan.isPending && <Spinner />}
      Plan again
    </Button>
  )

  return (
    <CloudPageHeader
      glyph={
        style ? (
          <ProjectIcon
            icon={style.icon}
            color={style.color}
            size="lg"
            className="size-11"
          />
        ) : (
          <DeploymentsIcon className="size-[22px]" />
        )
      }
      title={`${run.project?.name ?? "Deploy"} to ${run.environment}`}
      status={
        <StatusPill tone={toneOf(run.status)}>{runStatusWord(run)}</StatusPill>
      }
      meta={
        <span className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
          {kind && (
            <>
              <span className="font-medium text-foreground">{kind}</span>
              <span aria-hidden="true">·</span>
            </>
          )}
          <span title={formatDate(run.createdAt)}>
            Planned by <span className="text-foreground">{author.name}</span>{" "}
            {relative(Date.parse(run.createdAt), now)}
          </span>
          {p.sources.map((s) => (
            <span key={s.sourceId} className="contents">
              <span aria-hidden="true">·</span>
              <a
                href={commitURL(s.repository, s.commitSha)}
                target="_blank"
                rel="noreferrer"
                className={cn(
                  "underline-offset-4 hover:underline",
                  !s.inScope && "opacity-60"
                )}
                title={
                  s.inScope ? "Moves in this plan" : "Held at what's deployed"
                }
              >
                {s.repository} @{" "}
                <span className="font-mono text-foreground">
                  {s.commitSha.slice(0, 7)}
                </span>
              </a>
            </span>
          ))}
          <span aria-hidden="true">·</span>
          <span>loomplane.yaml revision {p.manifestRevision}</span>
        </span>
      }
      actions={
        <>
          {d && <CancelDeployment deployment={d} />}
          {rollback && d ? (
            <RollbackOffer
              run={run}
              deploymentId={d.id}
              // A deployed run offers Roll back in place of Plan again.
              fallback={run.status === "succeeded" ? planAgainButton : null}
            />
          ) : null}
          {run.status !== "succeeded" || !rollback ? planAgainButton : null}
          <PageMenu label="More deployment actions">
            <DropdownMenuItem
              render={
                <Link to="/projects/$id" params={{ id: run.projectId }} />
              }
            >
              Open {projectName}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard?.writeText(run.id)
                toast.success("Copied the plan ID")
              }}
            >
              Copy plan ID
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard?.writeText(api)
                toast.success("Copied the API request")
              }}
            >
              Copy as API request
            </DropdownMenuItem>
          </PageMenu>
        </>
      }
    />
  )
}

// A deployment that has finished, one way or another. Rolling back waits
// for a running one to end.
const finishedStatuses = new Set(["succeeded", "failed", "cancelled"])

/**
 * mayOfferRollback reports whether the run's deployment could be rolled
 * back from here at all: it's finished, it isn't a rollback itself, and
 * the person may deploy. RollbackOffer asks the server the rest.
 */
function mayOfferRollback(run: Run, canDeploy: boolean): boolean {
  const d = run.deployment
  return (
    !!d &&
    canDeploy &&
    finishedStatuses.has(d.status) &&
    run.plan.kind !== "rollback"
  )
}

/**
 * RollbackOffer shows Roll back when the server would roll the deployment
 * back: it's the environment's newest release. Cloud sync and catalog
 * deployments after it don't count, as they don't for the server. Until
 * then, or when it wouldn't, it shows the fallback.
 */
function RollbackOffer({
  run,
  deploymentId,
  fallback,
}: {
  run: Run
  deploymentId: string
  fallback?: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const target = useQuery(useRollbacksTarget, {
    path: { id: run.projectId, environment: run.environment },
    query: { deployment_id: deploymentId },
  })
  if (target.data === undefined) return fallback ?? null
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        Roll back
      </Button>
      <RollbackDialog
        open={open}
        onOpenChange={setOpen}
        projectId={run.projectId}
        environment={run.environment}
        deploymentId={deploymentId}
      />
    </>
  )
}

/** The phases the progress strip draws, in order. */
const phaseNames = ["Plan", "Approve", "Deploy"] as const
type Phase = (typeof phaseNames)[number]

/** stepTone is a thread step's state in the status tones. */
function stepTone(state: ThreadStep["state"]): Tone {
  switch (state) {
    case "done":
      return "live"
    case "running":
      return "flight"
    case "waiting":
      return "waiting"
    case "failed":
      return "failed"
    default:
      return "quiet"
  }
}

/**
 * phaseOf sums up one phase of a run for the progress strip: how it's
 * doing and the few words under its name.
 */
function phaseOf(
  run: Run,
  phase: Phase,
  now: number,
  nameOf: (id: string) => string
): { tone: Tone; detail: string; target: string } {
  const p = run.plan
  const steps = run.thread.filter((s) => s.group === phase)
  if (phase === "Plan") {
    const s = steps[0]
    const took =
      p.plannedAt && duration(Date.parse(p.plannedAt) - Date.parse(p.createdAt))
    return {
      tone: stepTone(s?.state ?? "pending"),
      detail:
        s?.state === "running"
          ? "Working it out"
          : s?.state === "failed"
            ? "Planning failed"
            : took
              ? `Done in ${took}`
              : "Done",
      target: "plan",
    }
  }
  if (phase === "Approve") {
    const s = steps[0]
    const state = s?.state ?? "pending"
    const decided =
      state === "done" && p.decidedAt
        ? `${p.decidedBy ? nameOf(p.decidedBy) : "Approved"}, ${relative(Date.parse(p.decidedAt), now)}`
        : "Approved"
    return {
      tone: stepTone(state),
      detail:
        state === "done"
          ? decided
          : state === "waiting"
            ? "Waiting for you"
            : state === "failed"
              ? "Blocked by problems"
              : state === "canceled"
                ? planStatus(p.status).label
                : "After planning",
      target: "plan",
    }
  }
  const d = run.deployment
  const work = steps.filter((s) => s.action)
  const n = work.length
  const failedAt = work.findIndex((s) => s.state === "failed")
  const runningAt = work.findIndex((s) => s.state === "running")
  const head =
    work[failedAt] ??
    work[runningAt] ??
    work.find((s) => s.state !== "done") ??
    work.at(-1)
  const target = head?.key ?? "all"
  if (n === 0)
    return {
      tone: stepTone(steps[0]?.state ?? "pending"),
      detail: "Nothing to change",
      target: "all",
    }
  if (run.status === "paused")
    return { tone: "waiting", detail: "Paused for capacity", target }
  if (failedAt >= 0)
    return {
      tone: "failed",
      detail: `Failed at step ${failedAt + 1} of ${n}`,
      target,
    }
  if (d?.status === "cancelled")
    return { tone: "quiet", detail: "Cancelled", target }
  if (d?.status === "succeeded" || work.every((s) => s.state === "done")) {
    const took =
      d?.finishedAt &&
      duration(Date.parse(d.finishedAt) - Date.parse(d.startedAt))
    return { tone: "live", detail: took ? `Done in ${took}` : "Done", target }
  }
  if (d) {
    const at =
      runningAt >= 0 ? runningAt : work.findIndex((s) => s.state !== "done")
    return {
      tone: "flight",
      detail: `Step ${Math.max(at, 0) + 1} of ${n} · ${duration(now - Date.parse(d.startedAt))}`,
      target,
    }
  }
  if (work.every((s) => s.state === "canceled"))
    return { tone: "quiet", detail: "Not run", target }
  return { tone: "quiet", detail: plural(n, "step"), target }
}

/**
 * ProgressStrip is the run in three phases: plan, approve, deploy. Picking
 * one opens its lines in the logs; Deploy opens the step it's on.
 */
function ProgressStrip({
  run,
  now,
  selected,
  onPick,
}: {
  run: Run
  now: number
  selected: string | null
  onPick: (source: string) => void
}) {
  const person = usePeople()
  return (
    <ol
      aria-label="Progress"
      className="grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] overflow-hidden rounded-xl bg-card ring-1 ring-border"
    >
      {phaseNames.map((name, i) => {
        const ph = phaseOf(run, name, now, (id) => person(id).name)
        const t = toneClass[ph.tone]
        const tinted =
          ph.tone === "waiting" || ph.tone === "flight" || ph.tone === "failed"
        return (
          <li
            key={name}
            className={cn("flex", i > 0 && "border-l", tinted && t.tint)}
          >
            <button
              type="button"
              aria-pressed={selected === ph.target}
              onClick={() => onPick(ph.target)}
              className="flex w-full min-w-0 items-center gap-2.5 px-4 py-3 text-left transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none aria-pressed:shadow-[inset_0_-2px_0_var(--primary)] motion-reduce:transition-none"
            >
              <span
                className={cn(
                  "flex size-[22px] shrink-0 items-center justify-center rounded-full font-mono text-[11px] font-medium",
                  ph.tone === "quiet"
                    ? "bg-muted text-muted-foreground"
                    : cn(t.dot, "text-white")
                )}
              >
                {i + 1}
              </span>
              <span className="grid min-w-0 gap-px">
                <span className="text-[13px] font-semibold">{name}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {ph.detail}
                </span>
              </span>
            </button>
          </li>
        )
      })}
    </ol>
  )
}

/**
 * FailureCallout names what failed: the plan, or the deployment step it
 * stopped at, with a retry from that step.
 */
function FailureCallout({ run }: { run: Run }) {
  const can = useProjectCan(run.projectId)
  const retry = useMutation(useDeploymentsRetry)
  const invalidate = useInvalidate()
  const connections = useQuery(useConnectionsList)
  if (run.status !== "failed") return null
  const p = run.plan
  const d = run.deployment
  if (!d)
    return (
      <CloudCallout tone="error" title="Planning failed">
        <span className="wrap-anywhere">
          {p.error || "Loomplane couldn't work out a plan."} Fix it, then plan
          again.
        </span>
      </CloudCallout>
    )
  const failed = run.thread.find(
    (s) => s.group === "Deploy" && s.state === "failed"
  )
  const notRun = run.thread
    .filter(
      (s) =>
        s.group === "Deploy" &&
        s.action &&
        s.state !== "done" &&
        s.state !== "failed"
    )
    .map((s) => s.action!.name)
  const name = failed?.action?.name
  const where = failed?.action
    ? whereOf(failed.action, connections.data?.connections ?? [])
    : ""

  const retryDeployment = async () => {
    try {
      await retry.mutateAsync({ path: { id: d.id } })
      invalidate(useDeploymentsForPlan)
      invalidate(useDeploymentsList)
      toast.success("Retrying from the step that failed")
    } catch (err) {
      toast.error(messageOf(err, "The deployment couldn't be retried."))
    }
  }

  return (
    <CloudCallout
      tone="error"
      title={
        name
          ? `${name} failed${where ? ` on ${where}` : ""}`
          : "The deployment failed"
      }
      action={
        d.status === "failed" &&
        can("deployment:approve") && (
          <Button
            size="sm"
            variant="outline"
            disabled={retry.isPending}
            onClick={() => void retryDeployment()}
          >
            {retry.isPending && <Spinner />}
            {name ? `Retry from ${name}` : "Retry"}
          </Button>
        )
      }
    >
      <span className="wrap-anywhere">
        {failed?.step?.message || d.error || "The provider gave no reason."}
        {notRun.length > 0 &&
          ` ${listOf(notRun)} didn't start, so ${run.environment} still runs the last release.`}
      </span>
    </CloudCallout>
  )
}

/** BlockedCallout says what stops a blocked plan and where to fix it. */
function BlockedCallout({ run }: { run: Run }) {
  const n = run.plan.problems.length
  return (
    <CloudCallout
      tone="error"
      title={`${n === 1 ? "One problem stops" : `${n} problems stop`} this plan from being approved`}
      action={
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={
            <Link
              to="/projects/$id"
              params={{ id: run.projectId }}
              search={{ tab: "deployments" }}
            />
          }
        >
          Fix them in the project&apos;s Deployments tab
        </Button>
      }
    >
      Each one is listed under Changes. Fix them, then plan again.
    </CloudCallout>
  )
}

/** listOf joins names as a person would: "a", "a and b", "a, b and c". */
function listOf(names: string[]): string {
  if (names.length <= 1) return names.join("")
  return `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`
}

/** providerLabel names where an action runs by its brand, not its key. */
function providerLabel(provider: string): string {
  const label = cloudLabel(provider)
  return label !== provider ? label : providerName(provider)
}

/**
 * whereOf is where an action runs: its account by name when it has one,
 * else the provider, then the region.
 */
function whereOf(a: Action, connections: Connection[]): string {
  const account = connections.find((c) => c.id === a.connectionId)?.name
  return [account ?? providerLabel(a.provider), a.region]
    .filter(Boolean)
    .join(" · ")
}

/** opTone is how a change reads at a glance: new, moving, kept or gone. */
const opTone: Record<string, Tone> = {
  create: "live",
  update: "flight",
  replace: "flight",
  delete: "failed",
  keep: "quiet",
}

/** kindOf says what an action is: a resource's kind or what it creates. */
function kindOf(a: Action): string {
  const kind = a.resource ? resourceLabel(a.resource.kind) : a.creates
  return a.resource?.version ? `${kind} ${a.resource.version}` : kind
}

function ChangesTab({ run }: { run: Run }) {
  const connections = useQuery(useConnectionsList)
  const p = run.plan
  const s = p.summary
  if (p.status === "planning")
    return (
      <CloudEmpty
        kind="compact"
        icon={LoaderCircleIcon}
        title={`Working out what ${run.environment} needs`}
      >
        The changes show here in a moment. Nothing changes in your clouds while
        Loomplane plans.
      </CloudEmpty>
    )
  if (p.status === "failed")
    return (
      <CloudEmpty kind="compact" title="No changes to show">
        Planning failed before it worked out any changes.
      </CloudEmpty>
    )
  const accounts = connections.data?.connections ?? []
  const kept = s.keep
  // A finding that a person can fix offers the fix beside it.
  const fix = (f: Finding) =>
    run.project && (
      <FindingActions
        finding={f}
        project={run.project}
        environment={run.environment}
      />
    )
  return (
    <div className="grid gap-6">
      {(p.problems.length > 0 || p.warnings.length > 0) && (
        <div className="grid gap-2.5">
          {p.problems.map((f, i) => (
            <CloudCallout
              key={`p${i}`}
              tone="error"
              title={f.message}
              action={fix(f)}
            >
              {f.path && <code className="font-mono text-xs">{f.path}</code>}
            </CloudCallout>
          ))}
          {p.warnings.map((f, i) => (
            <CloudCallout
              key={`w${i}`}
              tone="info"
              title="Worth a look"
              action={fix(f)}
            >
              {f.message}
              {f.path && (
                <code className="mt-0.5 block font-mono text-xs">{f.path}</code>
              )}
            </CloudCallout>
          ))}
        </div>
      )}
      <ReleaseKind plan={p} />
      <section aria-label="What this plan does" className="grid gap-2.5">
        <CloudSectionHead
          title="What this plan does"
          count={p.actions.length}
          description={
            p.actions.length === 0
              ? undefined
              : `${s.create} to create, ${s.update} to update, ${s.replace} to replace, ${s.delete} to delete and ${kept} kept, in the order it happens. Kept items show nothing else moves.`
          }
        />
        {p.actions.length === 0 ? (
          <CloudEmpty kind="compact" title="Nothing to change">
            What&apos;s deployed already matches loomplane.yaml.
          </CloudEmpty>
        ) : (
          <CloudTable
            label="Changes"
            columns="110px minmax(0,1fr) minmax(0,1.3fr) minmax(0,1.4fr)"
            head={["Change", "What", "Where", "Why"]}
            minWidth={760}
          >
            {[...p.actions]
              .sort((a, b) => a.step - b.step)
              .map((a) => (
                <CloudTableRow key={a.id}>
                  <Cell>
                    <StatusPill tone={opTone[a.op] ?? "quiet"}>
                      {opLabel(a.op)}
                    </StatusPill>
                  </Cell>
                  <NameCell name={a.name} meta={kindOf(a)} />
                  <Cell className="text-[12.5px]">{whereOf(a, accounts)}</Cell>
                  <Cell className="grid gap-1 text-[12.5px] leading-snug text-muted-foreground">
                    {a.reason && <span>{a.reason}</span>}
                    {a.changes?.map((c) => (
                      <span key={c.field} className="font-mono text-[11.5px]">
                        {c.field}: {c.from || "unset"} → {c.to || "unset"}
                      </span>
                    ))}
                  </Cell>
                </CloudTableRow>
              ))}
          </CloudTable>
        )}
      </section>
      <Artifacts artifacts={p.artifacts ?? []} />
      <Leftovers leftovers={p.leftovers ?? []} />
    </div>
  )
}

/**
 * logSources are the log viewer's choices: every line, the plan's own
 * record, and each step that changes a service, with how it's doing.
 */
function logSources(run: Run): LogSource[] {
  return [
    { id: "all", label: "Everything", steps: null },
    { id: "plan", label: "Plan", steps: ["plan", "approve"] },
    ...run.thread
      .filter((s) => s.group === "Deploy" && s.action)
      .map((s) => ({
        id: s.key,
        label: s.action!.name,
        steps: [s.key],
        tone: stepTone(s.state),
      })),
  ]
}

/** serviceState names how one step of the deployment went. */
function serviceState(s: ThreadStep): { word: string; tone: Tone } {
  switch (s.state) {
    case "done":
      return {
        word: s.action?.op === "delete" ? "Removed" : "Done",
        tone: "live",
      }
    case "running":
      return { word: s.detail ?? "Running", tone: "flight" }
    case "failed":
      return { word: "Failed", tone: "failed" }
    case "skipped":
      return { word: "Skipped", tone: "quiet" }
    case "canceled":
      return { word: "Not run", tone: "quiet" }
    default:
      return { word: "Not started", tone: "quiet" }
  }
}

function ServicesTab({
  run,
  onLogs,
}: {
  run: Run
  onLogs: (source: string) => void
}) {
  const connections = useQuery(useConnectionsList)
  const accounts = connections.data?.connections ?? []
  const steps = new Map(
    run.thread.filter((s) => s.action).map((s) => [s.key, s])
  )
  const actions = [...run.plan.actions].sort((a, b) => a.step - b.step)
  return (
    <section aria-label="Services in this deploy" className="grid gap-2.5">
      <CloudSectionHead
        title="Services"
        count={actions.length}
        description="Where each one runs, and its address."
      />
      {actions.length === 0 ? (
        <CloudEmpty kind="compact" title="No services in this plan">
          This plan changes nothing, so no service moves.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Services"
          columns="minmax(0,1fr) 150px minmax(0,1.2fr) minmax(0,1.2fr) 80px"
          head={["Service", "State", "Runs on", "Address", ""]}
          minWidth={760}
        >
          {actions.map((a) => {
            const s = steps.get(a.id)
            const state = s
              ? serviceState(s)
              : { word: "Kept", tone: "quiet" as Tone }
            return (
              <CloudTableRow key={a.id}>
                <NameCell name={a.name} meta={kindOf(a)} />
                <Cell>
                  <StatusPill tone={state.tone}>{state.word}</StatusPill>
                </Cell>
                <Cell className="text-[12.5px]">{whereOf(a, accounts)}</Cell>
                <Cell className="text-[12.5px] text-muted-foreground">
                  {s?.step?.url && s.state === "done" ? (
                    <ServiceAddress
                      address={s.step.url}
                      className="w-fit text-xs"
                    />
                  ) : (
                    "No address yet"
                  )}
                </Cell>
                <div role="cell" className="flex justify-end">
                  {s && (
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Logs for ${a.name}`}
                      onClick={() => onLogs(s.key)}
                    >
                      Logs
                    </Button>
                  )}
                </div>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
    </section>
  )
}

function DetailsTab({ run, now }: { run: Run; now: number }) {
  const person = usePeople()
  const connections = useQuery(useConnectionsList)
  const p = run.plan
  const services = planServices(p)
  const deploysWith = planConnections(p)
    .map(({ cloud, id }) => {
      const c = connections.data?.connections.find((x) => x.id === id)
      return `${c?.name ?? "A removed account"} (${cloudLabel(cloud)})`
    })
    .join(", ")
  const id = (value: string, label: string) => (
    <span className="inline-flex items-center gap-1">
      <Code>{value}</Code>
      <CopyButton value={value} label={`Copy the ${label} ID`} />
    </span>
  )
  const approval =
    p.decidedAt && (p.status === "approved" || run.deployment)
      ? `${p.decidedBy ? person(p.decidedBy).name : "Approved"}, ${relative(Date.parse(p.decidedAt), now)}`
      : p.status === "ready"
        ? p.expiresAt
          ? `Waiting until ${formatDate(p.expiresAt)}`
          : "Waiting for you"
        : p.status === "planning"
          ? "After planning"
          : planStatus(p.status).label
  const facts: { label: string; value: ReactNode }[] = [
    { label: "Plan", value: id(p.id, "plan") },
    {
      label: "Deployment",
      value: run.deployment
        ? id(run.deployment.id, "deployment")
        : p.status === "ready"
          ? "Starts on approval"
          : "Not started",
    },
    { label: "loomplane.yaml", value: `revision ${p.manifestRevision}` },
    ...p.sources.map((s) => ({
      label: s.repository,
      value: (
        <span>
          <Code>{s.commitSha.slice(0, 7)}</Code>
          {!s.inScope && (
            <span className="text-muted-foreground">
              , held at what&apos;s deployed
            </span>
          )}
        </span>
      ),
    })),
    {
      label: "Services",
      value: services.length > 0 ? services.join(", ") : "Every service",
    },
    { label: "Approval", value: approval },
    { label: "Cloud", value: deploysWith || "No account yet" },
    { label: "Planned", value: formatDate(p.createdAt) },
  ]
  return (
    <section aria-label="Details" className="grid max-w-[640px] gap-2.5">
      <CloudSectionHead
        title="Details"
        description="IDs and inputs, for support and the API."
      />
      <FactList facts={facts} />
    </section>
  )
}
