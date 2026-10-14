// One deployment, start to finish: the plan, the decision on it, and each
// step it took. A run is named by its plan's ID, because a plan exists from
// the moment someone asks for one and a deployment only once it's approved.
import {
  CheckIcon,
  ChevronRightIcon,
  CircleIcon,
  EllipsisIcon,
  MinusIcon,
  PauseIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  ShieldAlertIcon,
  TriangleAlertIcon,
  XIcon,
} from "lucide-react"
import { type CSSProperties, type ReactNode, useMemo, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDeploymentsForPlan,
  useDeploymentsList,
  useDeploymentsRetry,
  usePlansApprove,
  usePlansCreate,
  usePlansGet,
  usePlansList,
  usePlansReject,
} from "@/client/forge/loomplane/hooks"
import type { Action, Finding, Plan } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Page } from "@/components/page"
import {
  Code,
  CopyButton,
  EnvBadge,
  PersonAvatar,
  PhaseRuler,
  RunBadge,
  Thread,
  currentStep,
} from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { commitURL, resourceLabel } from "@/lib/architecture"
import { cloudLabel } from "@/lib/clouds"
import { dateTime, duration, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { scopeBody } from "@/lib/plans"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import { isLive, type Run, runLog, type ThreadStep } from "@/lib/runs"
import { BuildLogPanel } from "@/components/build-log-panel"
import { useNow, usePeople, useRunFor } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

export function DeploymentPage({ id }: { id: string }) {
  const plan = useQuery(usePlansGet, { path: { id } })
  if (plan.data) return <Loaded plan={plan.data} refetch={plan.refetch} />
  const missing = plan.status === "error" && statusOf(plan.error) === 404
  return (
    <>
      <SiteHeader
        crumbs={[{ label: "Deployments", to: "/deployments" }, { label: id }]}
      />
      <Page>
        {plan.status === "error" ? (
          missing ? (
            <Empty className="border border-dashed">
              <EmptyHeader>
                <EmptyTitle>No deployment here by that ID</EmptyTitle>
                <EmptyDescription>
                  It may belong to another organization, or the link is wrong.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Link to="/deployments" className={buttonVariants()}>
                  All deployments
                </Link>
              </EmptyContent>
            </Empty>
          ) : (
            <Alert variant="destructive">
              <AlertDescription>
                {messageOf(plan.error, "The deployment couldn't be loaded.")}
              </AlertDescription>
            </Alert>
          )
        ) : (
          <div className="grid gap-6" aria-busy="true">
            <Skeleton className="h-10 w-96" />
            <Skeleton className="h-36 rounded-xl" />
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
}: {
  plan: Plan
  refetch: () => Promise<unknown>
}) {
  const run = useRunFor(plan, refetch)
  const now = useNow()
  const person = usePeople()
  const lines = useMemo(() => runLog(run, (u) => person(u).name), [run, person])
  const [tab, setTab] = useState<string | null>(null)
  const [step, setStep] = useState<string | null>(null)
  const live = isLive(run)
  const activeTab =
    tab ??
    (run.status === "waiting" || run.status === "blocked" ? "plan" : "logs")
  const projectName = run.project?.name ?? "Project"

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Deployments", to: "/deployments" },
          { label: projectName },
          { label: run.environment },
        ]}
      />
      <Page className="gap-6">
        <Heading run={run} now={now} />
        <Progression run={run} now={now} />
        {run.status === "waiting" && <ApprovalAlert run={run} />}
        {run.status === "blocked" && <BlockedAlert run={run} />}
        <IdStrip run={run} now={now} />
        <div className="grid items-start gap-6 lg:grid-cols-[300px_minmax(0,1fr)]">
          <Steps
            run={run}
            now={now}
            selected={step}
            onSelect={(key) => {
              setStep((cur) => (cur === key ? null : key))
              setTab("logs")
            }}
          />
          <Tabs
            value={activeTab}
            onValueChange={(v) => setTab(String(v))}
            className="min-w-0 gap-4"
          >
            <TabsList variant="line">
              <TabsTrigger value="logs">
                Logs
                {live && (
                  <span className="size-1.5 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
                )}
              </TabsTrigger>
              <TabsTrigger value="plan">
                Plan
                <Badge
                  variant="secondary"
                  className="h-4.5 px-1.5 tabular-nums"
                >
                  {run.plan.actions.length}
                </Badge>
              </TabsTrigger>
              <TabsTrigger value="services">Services</TabsTrigger>
            </TabsList>
            <TabsContent value="logs">
              <BuildLogPanel
                activity={lines}
                deploymentId={run.deployment?.id}
                live={live}
                name={run.deployment?.id ?? run.id}
                step={step}
                stepName={run.thread.find((s) => s.key === step)?.name}
                onClearStep={() => setStep(null)}
              />
            </TabsContent>
            <TabsContent value="plan">
              <PlanTab run={run} />
            </TabsContent>
            <TabsContent value="services">
              <Services run={run} />
            </TabsContent>
          </Tabs>
        </div>
      </Page>
    </>
  )
}

function Heading({ run, now }: { run: Run; now: number }) {
  const { can } = useOrganization()
  const navigate = useNavigate()
  const person = usePeople()
  const replan = useMutation(usePlansCreate)
  const retry = useMutation(useDeploymentsRetry)
  const invalidate = useInvalidate()
  const author = person(run.createdBy)
  const style = run.project ? projectStyle(run.project) : null
  const d = run.deployment
  const moving = run.plan.sources.filter((s) => s.inScope)

  const planAgain = async () => {
    try {
      const p = await replan.mutateAsync({
        path: { id: run.projectId },
        body: {
          environment: run.environment,
          // Planning again moves what this plan moved and holds what it held.
          ...scopeBody(
            run.plan.sources.map((s) => s.alias),
            moving.map((s) => s.alias)
          ),
        },
      })
      invalidate(usePlansList)
      void navigate({ to: "/deployments/$id", params: { id: p.id } })
      toast.success(`Planning ${run.environment} again`)
    } catch (err) {
      toast.error(messageOf(err, "The plan couldn't start."))
    }
  }

  const retryDeployment = async () => {
    if (!d) return
    try {
      await retry.mutateAsync({ path: { id: d.id } })
      invalidate(useDeploymentsForPlan)
      invalidate(useDeploymentsList)
      toast.success("Retrying from the step that failed")
    } catch (err) {
      toast.error(messageOf(err, "The deployment couldn't be retried."))
    }
  }

  const api = `curl -H "Authorization: Bearer $LOOMPLANE_API_KEY" ${window.location.origin}/api/v1/plans/${run.id}`

  return (
    <header className="flex flex-col gap-5 md:flex-row md:items-start md:justify-between">
      <div className="grid min-w-0 gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <RunBadge status={run.status} />
          {run.project && style && (
            <Link
              to="/projects/$id"
              params={{ id: run.projectId }}
              className={cn(
                buttonVariants({ variant: "link", size: "sm" }),
                "h-auto gap-1.5 px-1 text-foreground"
              )}
            >
              <ProjectIcon icon={style.icon} color={style.color} size="xs" />
              {run.project.name}
            </Link>
          )}
          <EnvBadge env={run.environment} />
        </div>
        <h1 className="text-[28px] leading-tight font-semibold tracking-[-0.025em] text-balance md:text-[32px]">
          {run.project?.name ?? "Deploy"} to {run.environment}
        </h1>
        <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-sm text-muted-foreground">
          <span className="flex items-center gap-2">
            <PersonAvatar initials={author.initials} name={author.name} />
            <span>
              <span className="text-foreground">{author.name}</span> planned it
            </span>
          </span>
          {run.plan.sources.map((s) => (
            <a
              key={s.sourceId}
              href={commitURL(s.repository, s.commitSha)}
              target="_blank"
              rel="noreferrer"
              className={cn(
                "flex items-center gap-1.5 underline-offset-4 hover:underline",
                !s.inScope && "opacity-60"
              )}
              title={
                s.inScope ? "Moves in this plan" : "Held at what's deployed"
              }
            >
              {run.plan.sources.length > 1 && <span>{s.alias}</span>}
              <Code className="rounded-md bg-muted px-1.5 py-0.5 text-[12px] text-foreground">
                {s.commitSha.slice(0, 7)}
              </Code>
            </a>
          ))}
          <span title={dateTime(Date.parse(run.createdAt))}>
            Started {relative(Date.parse(run.createdAt), now)}
          </span>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {d?.status === "failed" && can("deployment:approve") && (
          <Button
            className="rounded-full"
            disabled={retry.isPending}
            onClick={() => void retryDeployment()}
          >
            {retry.isPending ? <Spinner /> : <RotateCcwIcon />}
            Retry
          </Button>
        )}
        {!isLive(run) && can("deployment:create") && (
          <Button
            variant="outline"
            className="rounded-full"
            disabled={replan.isPending}
            onClick={() => void planAgain()}
          >
            {replan.isPending ? <Spinner /> : <RefreshCwIcon />}
            Plan again
          </Button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="outline"
                size="icon"
                className="rounded-full"
                aria-label="More"
              />
            }
          >
            <EllipsisIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-60">
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
            <DropdownMenuSeparator />
            <DropdownMenuItem
              render={
                <Link to="/projects/$id" params={{ id: run.projectId }} />
              }
            >
              Open the project
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  )
}

/** Progression is the run's thread across the warp, phases below. */
function Progression({ run, now }: { run: Run; now: number }) {
  const steps = run.thread
  const step = currentStep(steps)
  const at = step ? steps.indexOf(step) : steps.length - 1
  // Where the thread is: mid-step while running, the step it stopped on
  // otherwise, the far end once it's live.
  const glowAt =
    run.status === "succeeded"
      ? 92
      : Math.min(
          ((at + (step?.state === "running" ? (step.progress ?? 0.5) : 0.5)) /
            steps.length) *
            100,
          96
        )
  const tone =
    run.status === "failed" || run.status === "blocked"
      ? "failed"
      : run.status === "succeeded" || run.status === "approved"
        ? "quiet"
        : undefined
  const done = steps.filter((s) => s.state === "done").length
  return (
    <Card className="relative overflow-hidden">
      {run.status !== "canceled" && (
        <div
          aria-hidden="true"
          data-tone={tone}
          className="loom-glow pointer-events-none absolute inset-0"
          style={{ "--glow-x": `${glowAt}%` } as CSSProperties}
        />
      )}
      <CardContent className="relative grid gap-4 py-2">
        <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1">
          <div className="flex items-baseline gap-2">
            <span className="text-lg font-semibold tracking-tight">
              {run.phase}
            </span>
            <span className="text-sm text-muted-foreground">
              {run.status === "succeeded"
                ? `all ${steps.length} steps`
                : `${done} of ${steps.length} steps done`}
            </span>
          </div>
          <div className="flex items-baseline gap-4 text-sm text-muted-foreground tabular-nums">
            <span>
              <span className="font-medium text-foreground">
                {duration(
                  (run.finishedAt ? Date.parse(run.finishedAt) : now) -
                    Date.parse(run.createdAt)
                )}
              </span>{" "}
              {run.finishedAt ? "in all" : "so far"}
            </span>
            {run.finishedAt && (
              <span>finished {relative(Date.parse(run.finishedAt), now)}</span>
            )}
          </div>
        </div>
        <Thread steps={steps} size="lg" tooltips />
        <PhaseRuler
          steps={steps}
          active={run.status === "succeeded" ? undefined : step?.group}
        />
      </CardContent>
    </Card>
  )
}

function IdStrip({ run, now }: { run: Run; now: number }) {
  const person = usePeople()
  const connections = useQuery(useConnectionsList)
  const p = run.plan
  const deploysWith = Object.entries(p.connections as Record<string, string>)
    .map(([cloud, id]) => {
      const c = connections.data?.connections.find((x) => x.id === id)
      return `${c?.name ?? "a removed connection"} (${cloudLabel(cloud)})`
    })
    .join(", ")
  const items: { label: string; value: ReactNode; copy?: string }[] = [
    { label: "Plan", value: <Code>{p.id}</Code>, copy: p.id },
    {
      label: "Deployment",
      value: run.deployment ? (
        <Code>{run.deployment.id}</Code>
      ) : (
        <span className="text-muted-foreground">Not started</span>
      ),
      copy: run.deployment?.id,
    },
    { label: "Manifest", value: `revision ${p.manifestRevision}` },
    {
      label: "Approval",
      value:
        p.decidedAt && (p.status === "approved" || run.deployment)
          ? `${p.decidedBy ? person(p.decidedBy).name.split(" ")[0] : "Approved"}, ${relative(Date.parse(p.decidedAt), now)}`
          : p.status === "ready"
            ? p.expiresAt
              ? `Waiting until ${dateTime(Date.parse(p.expiresAt))}`
              : "Waiting"
            : p.status === "rejected"
              ? "Rejected"
              : p.status === "blocked"
                ? "Blocked"
                : p.status === "planning"
                  ? "After planning"
                  : "None",
    },
    { label: "Deploys with", value: deploysWith || "No connection yet" },
  ]
  return (
    <dl className="grid grid-cols-2 border-y sm:grid-cols-3 lg:grid-cols-5">
      {items.map((it, i) => (
        <div
          key={it.label}
          className={cn(
            "grid min-w-0 gap-1 py-3.5 pr-4",
            i > 0 && "lg:border-l lg:pl-5"
          )}
        >
          <dt className="text-xs text-muted-foreground">{it.label}</dt>
          <dd className="flex min-w-0 items-center gap-1 text-sm">
            <span className="truncate">{it.value}</span>
            {it.copy && (
              <CopyButton
                value={it.copy}
                label={`Copy the ${it.label.toLowerCase()} ID`}
              />
            )}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function ApprovalAlert({ run }: { run: Run }) {
  const { can } = useOrganization()
  const approve = useMutation(usePlansApprove)
  const reject = useMutation(usePlansReject)
  const invalidate = useInvalidate()
  const [rejecting, setRejecting] = useState(false)
  const [note, setNote] = useState("")
  const p = run.plan
  const s = p.summary
  const busy = approve.isPending || reject.isPending
  const removes = s.delete + s.replace

  const decide = async (yes: boolean) => {
    try {
      const body = { revision: p.revision, note: note.trim() || undefined }
      const out = yes
        ? await approve.mutateAsync({ path: { id: p.id }, body })
        : await reject.mutateAsync({ path: { id: p.id }, body })
      invalidate(usePlansList)
      invalidate(usePlansGet)
      setRejecting(false)
      toast.success(
        out.status === "stale"
          ? "What's deployed changed since this plan was made. Plan again."
          : yes
            ? p.executable
              ? "Approved. Deploying now."
              : "Approved"
            : "Plan rejected"
      )
    } catch (err) {
      toast.error(messageOf(err, "The decision wasn't recorded."))
    }
  }

  return (
    <Alert className="items-center gap-y-1 rounded-xl border-weld/50 bg-[color-mix(in_oklch,var(--weld)_6%,var(--card))] p-4 has-[>svg]:gap-x-3 md:has-[>svg]:grid-cols-[auto_1fr_auto]">
      <ShieldAlertIcon className="text-weld-ink size-5!" />
      <AlertTitle>
        {removes > 0
          ? `This plan replaces or removes ${removes} ${removes === 1 ? "thing" : "things"}, so it waits for an approval.`
          : `Deploying to ${run.environment} waits for someone to approve the plan.`}
      </AlertTitle>
      <AlertDescription className="col-start-2">
        {s.create} to create, {s.update} to update, {s.replace} to replace,{" "}
        {s.delete} to delete. Nothing has changed in your clouds yet.
        {p.warnings.length > 0 &&
          ` ${p.warnings.length} ${p.warnings.length === 1 ? "warning is" : "warnings are"} worth a look in the plan.`}
      </AlertDescription>
      {can("deployment:approve") && (
        <div className="col-start-2 mt-2 flex gap-2 md:col-start-3 md:row-span-2 md:row-start-1 md:mt-0">
          <Button
            variant="outline"
            className="rounded-full"
            disabled={busy}
            onClick={() => setRejecting(true)}
          >
            Reject
          </Button>
          <Button
            className="rounded-full"
            disabled={busy}
            onClick={() => void decide(true)}
          >
            {approve.isPending ? <Spinner /> : <CheckIcon />}
            {p.executable ? "Approve and deploy" : "Approve"}
          </Button>
        </div>
      )}
      <AlertDialog open={rejecting} onOpenChange={setRejecting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Reject this plan?</AlertDialogTitle>
            <AlertDialogDescription>
              Nothing changes in your clouds. Anyone can plan {run.environment}{" "}
              again once the manifest or the code is right.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Textarea
            aria-label="Why"
            placeholder="Why, for the record (optional)"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            maxLength={2000}
          />
          <AlertDialogFooter>
            <AlertDialogCancel>Keep reviewing</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={busy}
              onClick={() => void decide(false)}
            >
              {reject.isPending && <Spinner />}
              Reject plan
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Alert>
  )
}

function BlockedAlert({ run }: { run: Run }) {
  const n = run.plan.problems.length
  return (
    <Alert variant="destructive" className="rounded-xl p-4">
      <TriangleAlertIcon />
      <AlertTitle>
        {n === 1 ? "One problem stops" : `${n} problems stop`} this plan from
        being approved
      </AlertTitle>
      <AlertDescription>
        <ul className="grid list-disc gap-1 pl-4">
          {run.plan.problems.map((f, i) => (
            <li key={`${f.code}:${i}`}>{f.message}</li>
          ))}
        </ul>
        <Link
          to="/projects/$id"
          params={{ id: run.projectId }}
          className="mt-2 inline-block font-medium underline underline-offset-4"
        >
          Fix them in the project&apos;s Deploy tab
        </Link>
      </AlertDescription>
    </Alert>
  )
}

function StepIcon({ s }: { s: ThreadStep }) {
  const box =
    "relative z-10 flex size-5 shrink-0 items-center justify-center rounded-full ring-4 ring-card"
  switch (s.state) {
    case "done":
      return (
        <span className={cn(box, "bg-overdye text-white")}>
          <CheckIcon className="size-3" strokeWidth={3} />
        </span>
      )
    case "running":
      return (
        <span className={cn(box, "shuttle-glow bg-weld text-white")}>
          <Spinner className="size-3" />
        </span>
      )
    case "waiting":
      return (
        <span className={cn(box, "text-weld-ink border-2 border-weld bg-card")}>
          <PauseIcon className="size-2.5" fill="currentColor" />
        </span>
      )
    case "failed":
      return (
        <span className={cn(box, "bg-destructive text-white")}>
          <XIcon className="size-3" strokeWidth={3} />
        </span>
      )
    case "canceled":
    case "skipped":
      return (
        <span className={cn(box, "bg-muted text-muted-foreground")}>
          <MinusIcon className="size-3" strokeWidth={3} />
        </span>
      )
    default:
      return (
        <span className={cn(box, "bg-card text-muted-foreground/40")}>
          <CircleIcon className="size-3" />
        </span>
      )
  }
}

function Steps({
  run,
  now,
  selected,
  onSelect,
}: {
  run: Run
  now: number
  selected: string | null
  onSelect: (key: string) => void
}) {
  const groups = [...new Set(run.thread.map((s) => s.group))]
  return (
    <Card className="gap-2 lg:sticky lg:top-16">
      <CardHeader>
        <CardTitle className="text-sm">Steps</CardTitle>
        <CardDescription className="text-xs">
          Pick one to filter the logs
        </CardDescription>
      </CardHeader>
      <ol className="grid px-2">
        {groups.map((g) => (
          <li key={g} className="grid">
            <span className="px-2 pt-3 pb-1 text-xs text-muted-foreground">
              {g}
            </span>
            <ol className="relative grid">
              <span
                aria-hidden="true"
                className="absolute top-4 bottom-4 left-[19px] w-px bg-border"
              />
              {run.thread
                .filter((s) => s.group === g)
                .map((s) => {
                  const reached =
                    s.state !== "pending" && s.state !== "canceled"
                  const time =
                    s.startedAt && s.finishedAt
                      ? duration(
                          Date.parse(s.finishedAt) - Date.parse(s.startedAt)
                        )
                      : s.state === "running" && s.startedAt
                        ? duration(now - Date.parse(s.startedAt))
                        : ""
                  return (
                    <li key={s.key}>
                      <Button
                        variant="ghost"
                        disabled={!reached}
                        aria-pressed={selected === s.key}
                        onClick={() => onSelect(s.key)}
                        title={s.detail}
                        className="h-auto w-full justify-start gap-3 px-2 py-2 text-left font-normal disabled:text-muted-foreground disabled:opacity-100 aria-pressed:bg-muted"
                      >
                        <StepIcon s={s} />
                        <span
                          className={cn(
                            "flex-1 truncate",
                            (s.state === "running" || s.state === "waiting") &&
                              "font-medium",
                            s.state === "failed" &&
                              "font-medium text-destructive"
                          )}
                        >
                          {s.name}
                        </span>
                        <span className="text-xs text-muted-foreground tabular-nums">
                          {time}
                        </span>
                      </Button>
                    </li>
                  )
                })}
            </ol>
          </li>
        ))}
      </ol>
    </Card>
  )
}

const opStyle: Record<
  string,
  { icon: typeof PlusIcon; label: string; tone: string }
> = {
  create: {
    icon: PlusIcon,
    label: "Create",
    tone: "text-overdye bg-overdye/12",
  },
  update: {
    icon: PencilIcon,
    label: "Update",
    tone: "text-muted-foreground bg-muted",
  },
  replace: {
    icon: RefreshCwIcon,
    label: "Replace",
    tone: "text-weld-ink bg-weld/15",
  },
  delete: {
    icon: MinusIcon,
    label: "Delete",
    tone: "text-destructive bg-destructive/12",
  },
  keep: {
    icon: CheckIcon,
    label: "Keep",
    tone: "text-muted-foreground bg-muted",
  },
}

function PlanTab({ run }: { run: Run }) {
  const p = run.plan
  const s = p.summary
  if (p.status === "planning") {
    return (
      <Card className="py-10">
        <p className="flex items-center justify-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Working out what {run.environment} needs
        </p>
      </Card>
    )
  }
  if (p.status === "failed") {
    return (
      <Alert variant="destructive">
        <AlertTitle>Planning failed</AlertTitle>
        <AlertDescription className="wrap-anywhere">{p.error}</AlertDescription>
      </Alert>
    )
  }
  return (
    <div className="grid gap-4">
      <Card className="gap-0 py-0">
        <CardHeader className="flex flex-wrap items-center gap-x-5 gap-y-2 border-b py-4">
          {(["create", "update", "replace", "delete"] as const).map((op) => {
            const Icon = opStyle[op]?.icon ?? CircleIcon
            return (
              <span key={op} className="flex items-center gap-2 text-sm">
                <span
                  className={cn(
                    "flex size-5 items-center justify-center rounded-md",
                    opStyle[op]?.tone
                  )}
                >
                  <Icon className="size-3" strokeWidth={2.5} />
                </span>
                <span className="tabular-nums">{s[op]}</span>
                <span className="text-muted-foreground">to {op}</span>
              </span>
            )
          })}
          <span className="ml-auto text-sm text-muted-foreground">
            {s.keep} kept as they are
          </span>
        </CardHeader>
        {p.actions.length === 0 ? (
          <p className="px-5 py-8 text-center text-sm text-muted-foreground">
            Nothing to do: what&apos;s deployed already matches the manifest.
          </p>
        ) : (
          <div className="divide-y">
            {[...p.actions]
              .sort((a, b) => a.step - b.step)
              .map((a) => (
                <ActionRow key={a.id} action={a} />
              ))}
          </div>
        )}
      </Card>
      {(p.problems.length > 0 || p.warnings.length > 0) && (
        <Card className="gap-3">
          <CardHeader>
            <CardTitle className="text-sm">Findings</CardTitle>
            <CardDescription className="text-xs">
              Problems stop the plan from being approved; warnings are worth a
              look. Fix them in the project&apos;s Deploy tab.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ul className="grid gap-3">
              {p.problems.map((f, i) => (
                <FindingRow key={`p${i}`} finding={f} tone="bad" />
              ))}
              {p.warnings.map((f, i) => (
                <FindingRow key={`w${i}`} finding={f} tone="warn" />
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function FindingRow({
  finding: f,
  tone,
}: {
  finding: Finding
  tone: "bad" | "warn"
}) {
  return (
    <li className="flex gap-2.5">
      <span
        aria-hidden="true"
        className={cn(
          "mt-1.5 size-1.5 shrink-0 rounded-full",
          tone === "bad" ? "bg-destructive" : "bg-weld"
        )}
      />
      <div className="grid min-w-0 gap-1">
        <p className="text-sm">{f.message}</p>
        {f.path && (
          <code className="font-mono text-xs text-muted-foreground">
            {f.path}
          </code>
        )}
      </div>
    </li>
  )
}

function ActionRow({ action: a }: { action: Action }) {
  const style = opStyle[a.op] ?? opStyle.update
  const Icon = style?.icon ?? CircleIcon
  const facts: [string, string][] = [
    ["provider", a.provider],
    ...(a.region ? ([["region", a.region]] as [string, string][]) : []),
    ...(a.service?.image
      ? ([["image", a.service.image]] as [string, string][])
      : []),
    ...(a.service?.port
      ? ([["port", String(a.service.port)]] as [string, string][])
      : []),
    ...(a.resource?.version
      ? ([["version", a.resource.version]] as [string, string][])
      : []),
  ]
  return (
    <Collapsible defaultOpen={a.op !== "keep" && a.op !== "update"}>
      <CollapsibleTrigger className="group/change flex w-full items-center gap-3 px-5 py-3.5 text-left text-sm outline-none hover:bg-muted/50 focus-visible:bg-muted/50">
        <span
          className={cn(
            "flex size-5 shrink-0 items-center justify-center rounded-md",
            style?.tone
          )}
        >
          <Icon className="size-3" strokeWidth={2.5} />
          <span className="sr-only">{style?.label}</span>
        </span>
        <span className="min-w-0 flex-1 truncate">
          <span className="text-muted-foreground">
            {a.resource ? resourceLabel(a.resource.kind) : a.creates}
          </span>{" "}
          <span className="font-mono font-medium">{a.name}</span>
        </span>
        <span className="text-xs text-muted-foreground">
          {a.provider}
          {a.region ? `, ${a.region}` : ""}
        </span>
        <ChevronRightIcon className="size-3.5 text-muted-foreground transition-transform group-data-[panel-open]/change:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mx-5 mb-4 grid gap-3">
          {a.reason && (
            <p className="text-xs text-muted-foreground">{a.reason}</p>
          )}
          <dl className="grid grid-cols-[minmax(7rem,auto)_1fr] gap-x-6 gap-y-1.5 rounded-lg bg-(--terminal) p-4 font-mono text-[12px] text-(--terminal-foreground)">
            {facts.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-(--terminal-muted)">{k}</dt>
                <dd className="break-all">{v}</dd>
              </div>
            ))}
            {a.changes?.map((c) => (
              <div key={c.field} className="contents">
                <dt className="text-(--terminal-muted)">{c.field}</dt>
                <dd className="flex flex-wrap items-center gap-x-2">
                  <span className="text-[color-mix(in_oklch,var(--destructive)_60%,white)] line-through">
                    {c.from || "unset"}
                  </span>
                  <span className="text-[color-mix(in_oklch,var(--overdye)_55%,white)]">
                    + {c.to || "unset"}
                  </span>
                </dd>
              </div>
            ))}
          </dl>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

function Services({ run }: { run: Run }) {
  const deploy = run.thread.filter((s) => s.group === "Deploy" && s.action)
  if (deploy.length === 0) {
    return (
      <Card className="py-0">
        <p className="px-5 py-8 text-center text-sm text-muted-foreground">
          This plan changes nothing, so no service moves.
        </p>
      </Card>
    )
  }
  return (
    <Card className="py-2">
      <ItemGroup className="gap-0">
        {deploy.map((s, i) => {
          const a = s.action as Action
          const state =
            s.state === "done"
              ? a.op === "delete"
                ? "Removed"
                : "Released"
              : s.state === "running"
                ? (s.detail ?? "Rolling out")
                : s.state === "failed"
                  ? "Failed"
                  : s.state === "skipped"
                    ? "Skipped"
                    : s.state === "canceled"
                      ? "Not run"
                      : "Queued"
          return (
            <div key={s.key}>
              {i > 0 && <ItemSeparator className="my-0" />}
              <Item className="rounded-none px-5">
                <ItemContent>
                  <ItemTitle>{a.name}</ItemTitle>
                  <ItemDescription className="line-clamp-1 text-xs">
                    {a.resource ? resourceLabel(a.resource.kind) : a.creates} on{" "}
                    {a.provider}
                    {a.region ? `, ${a.region}` : ""}
                  </ItemDescription>
                  {s.step?.url && s.state === "done" && (
                    <a
                      href={s.step.url}
                      target="_blank"
                      rel="noreferrer"
                      className="w-fit text-xs underline-offset-4 hover:underline"
                    >
                      {s.step.url.replace(/^https?:\/\//, "")}
                    </a>
                  )}
                </ItemContent>
                <ItemActions
                  className={cn(
                    "gap-1.5 text-xs",
                    s.state === "running"
                      ? "text-weld-ink font-medium"
                      : "text-muted-foreground"
                  )}
                >
                  <span
                    className={cn(
                      "size-1.5 rounded-full",
                      s.state === "done" && "bg-overdye",
                      s.state === "running" && "animate-pulse bg-weld",
                      s.state === "failed" && "bg-destructive",
                      (s.state === "pending" ||
                        s.state === "canceled" ||
                        s.state === "skipped") &&
                        "bg-warp"
                    )}
                  />
                  {state}
                </ItemActions>
              </Item>
            </div>
          )
        })}
      </ItemGroup>
    </Card>
  )
}
