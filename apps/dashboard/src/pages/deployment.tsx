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
  TriangleAlertIcon,
  XIcon,
} from "lucide-react"
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
} from "@/client/forge/loomplane/hooks"
import type { Action, Finding, Plan } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { FailedDeploymentCleanup } from "@/components/failed-deployment-cleanup"
import { ApprovalCard } from "@/components/approval"
import { CanaryPanel } from "@/components/canary"
import { showsCanary } from "@/lib/canary"
import { Page } from "@/components/page"
import {
  Code,
  CopyButton,
  EnvBadge,
  PersonAvatar,
  stateLabel,
  StatusPill,
  toneOf,
  Thread,
  currentStep,
} from "@/components/runs"
import { ServiceAddress } from "@/components/service-address"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import { commitURL, resourceLabel } from "@/lib/architecture"
import { cloudLabel } from "@/lib/clouds"
import { dateTime, duration, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { scopeBody, planConnections } from "@/lib/plans"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import {
  isLive,
  type Run,
  runLog,
  runStatusText,
  type ThreadStep,
} from "@/lib/runs"
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
            <Empty preview={false}>
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
  const { can } = useOrganization()
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
      <Page>
        <Heading run={run} now={now} />
        {plan.catalog &&
          plan.actions.length === 1 &&
          plan.actions[0].provider === "cloud-sql" &&
          plan.actions[0].op === "create" &&
          run.deployment &&
          ["failed", "cancelled"].includes(run.deployment.status) && (
            <FailedDeploymentCleanup
              id={run.deployment.id}
              canApprove={can("deployment:approve")}
              onChanged={refetch}
            />
          )}
        <Progression
          run={run}
          now={now}
          selected={step}
          onSelect={(key) => {
            setStep((cur) => (cur === key ? null : key))
            setTab("logs")
          }}
        />
        {showsCanary(run) && (
          <CanaryPanel
            deploymentId={run.deployment?.id}
            release={run.plan.release}
          />
        )}
        {run.status === "waiting" && <ApprovalCard run={run} />}
        {run.status === "blocked" && <BlockedAlert run={run} />}
        <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
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
                <TabsCount>{run.plan.actions.length}</TabsCount>
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
          <aside className="grid gap-4 lg:sticky lg:top-16">
            <IdStrip run={run} now={now} />
            <Steps
              run={run}
              now={now}
              selected={step}
              onSelect={(key) => {
                setStep((cur) => (cur === key ? null : key))
                setTab("logs")
              }}
            />
          </aside>
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
          <StatusPill tone={toneOf(run.status)}>
            {runStatusText[run.status]}
          </StatusPill>
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
        <h1 className="text-[22px] leading-tight font-semibold tracking-[-0.025em] text-balance md:text-2xl">
          {run.project?.name ?? "Deploy"} to {run.environment}
        </h1>
        <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-[13px] text-muted-foreground">
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
const groupState = (steps: ThreadStep[]): ThreadStep["state"] =>
  steps.some((s) => s.state === "failed")
    ? "failed"
    : steps.some((s) => s.state === "running")
      ? "running"
      : steps.some((s) => s.state === "waiting")
        ? "waiting"
        : steps.every((s) => s.state === "done" || s.state === "skipped")
          ? "done"
          : steps.some((s) => s.state === "canceled")
            ? "canceled"
            : "pending"

const groupTone: Record<ThreadStep["state"], string> = {
  done: "text-overdye-ink",
  running: "text-flight-ink",
  waiting: "text-weld-ink",
  failed: "text-destructive",
  canceled: "text-muted-foreground",
  pending: "text-muted-foreground",
  skipped: "text-muted-foreground",
}

/**
 * Progression is the run's phases side by side: each one's thread, where it
 * stands and how long it took. Picking one filters the logs to its current
 * step.
 */
function Progression({
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
  const steps = run.thread
  const done = steps.filter((s) => s.state === "done").length
  const groups = [...new Set(steps.map((s) => s.group))]
  return (
    <section
      aria-label="Steps"
      className="overflow-hidden rounded-xl bg-card ring-1 ring-border"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1 border-b px-4 py-2.5">
        <span className="text-[13.5px] font-semibold">
          {run.status === "succeeded"
            ? `All ${steps.length} steps done`
            : `${done} of ${steps.length} steps done`}
          <span className="ml-2 font-normal text-muted-foreground">
            {run.phase}
          </span>
        </span>
        <span className="text-[12.5px] text-muted-foreground tabular-nums">
          <span className="font-mono text-foreground">
            {duration(
              (run.finishedAt ? Date.parse(run.finishedAt) : now) -
                Date.parse(run.createdAt)
            )}
          </span>{" "}
          {run.finishedAt
            ? `in all, finished ${relative(Date.parse(run.finishedAt), now)}`
            : "so far"}
        </span>
      </div>
      <div className="flex flex-wrap">
        {groups.map((g, i) => {
          const inGroup = steps.filter((s) => s.group === g)
          const state = groupState(inGroup)
          const head = currentStep(inGroup) ?? inGroup[0]
          const on = inGroup.some((s) => s.key === selected)
          const first = inGroup[0]?.startedAt
          const last = inGroup.at(-1)?.finishedAt
          const took =
            first && last && state === "done"
              ? duration(Date.parse(last) - Date.parse(first))
              : undefined
          return (
            <button
              key={g}
              type="button"
              aria-pressed={on}
              onClick={() => head && onSelect(head.key)}
              className={cn(
                "flex min-w-[200px] flex-1 flex-col gap-1.5 px-4 pt-3 pb-3.5 text-left transition-colors hover:bg-subtle",
                i > 0 && "border-l",
                on && "bg-brand-soft shadow-[inset_0_0_0_2px_var(--brand-line)]"
              )}
            >
              <Thread steps={inGroup} size="sm" />
              <span className="mt-1 text-[11px] font-semibold tracking-[0.05em] text-muted-foreground uppercase">
                {g}
                {inGroup.length > 1 && ` · ${inGroup.length} steps`}
              </span>
              <span className="truncate text-[13px] font-medium">
                {head?.name ?? g}
              </span>
              <span className={cn("text-[12.5px]", groupTone[state])}>
                {took ? `Took ${took}` : (head?.detail ?? stateLabel[state])}
              </span>
            </button>
          )
        })}
      </div>
    </section>
  )
}

function IdStrip({ run, now }: { run: Run; now: number }) {
  const person = usePeople()
  const connections = useQuery(useConnectionsList)
  const p = run.plan
  const deploysWith = planConnections(p)
    .map(({ cloud, id }) => {
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
    <section className="rounded-xl bg-card px-4 pt-1 pb-1.5 ring-1 ring-border">
      <h2 className="pt-3 pb-2 text-[13px] font-semibold">Details</h2>
      <dl className="grid">
        {items.map((it) => (
          <div
            key={it.label}
            className="grid min-w-0 grid-cols-[96px_minmax(0,1fr)] items-center gap-2 border-t py-2"
          >
            <dt className="text-[12.5px] text-muted-foreground">{it.label}</dt>
            <dd className="flex min-w-0 items-center gap-1 text-[13px]">
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
    </section>
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
        <span className={cn(box, "border-2 border-weld bg-card text-weld-ink")}>
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
    <Card className="gap-2">
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
    tone: "text-overdye-ink bg-overdye-tint",
  },
  update: {
    icon: PencilIcon,
    label: "Update",
    tone: "text-muted-foreground bg-muted",
  },
  replace: {
    icon: RefreshCwIcon,
    label: "Replace",
    tone: "text-weld-ink bg-weld-tint",
  },
  delete: {
    icon: MinusIcon,
    label: "Delete",
    tone: "text-destructive bg-failed-tint",
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
        <p className="flex items-center justify-center gap-2 text-[13px] text-muted-foreground">
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
              <span key={op} className="flex items-center gap-2 text-[13px]">
                <span
                  className={cn(
                    "flex size-5 items-center justify-center rounded-md",
                    opStyle[op]?.tone
                  )}
                >
                  <Icon className="size-3" strokeWidth={2.5} />
                </span>
                <span className="font-mono tabular-nums">{s[op]}</span>
                <span className="text-muted-foreground">to {op}</span>
              </span>
            )
          })}
          <span className="ml-auto text-[13px] text-muted-foreground">
            {s.keep} kept as they are
          </span>
        </CardHeader>
        {p.actions.length === 0 ? (
          <p className="px-5 py-8 text-center text-[13px] text-muted-foreground">
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
        <p className="text-[13px]">{f.message}</p>
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
      <CollapsibleTrigger className="group/change flex w-full items-center gap-3 px-5 py-3.5 text-left text-[13px] outline-none hover:bg-muted/50 focus-visible:bg-muted/50">
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
        <p className="px-5 py-8 text-center text-[13px] text-muted-foreground">
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
                    <ServiceAddress
                      address={s.step.url}
                      className="w-fit text-xs"
                    />
                  )}
                </ItemContent>
                <ItemActions
                  className={cn(
                    "gap-1.5 text-xs",
                    s.state === "running"
                      ? "font-medium text-weld-ink"
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
