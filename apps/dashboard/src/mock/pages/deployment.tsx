import {
  CheckIcon,
  ChevronRightIcon,
  CircleIcon,
  EllipsisIcon,
  GitBranchIcon,
  MinusIcon,
  PauseIcon,
  PencilIcon,
  PlusIcon,
  RotateCcwIcon,
  ShieldAlertIcon,
  XIcon,
} from "lucide-react"
import { type CSSProperties, type ReactNode, useMemo, useState } from "react"
import { toast } from "sonner"

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
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
  ItemMedia,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"
import {
  Code,
  CopyButton,
  EnvBadge,
  PersonAvatar,
  ProviderGlyph,
  StatusBadge,
} from "@/mock/components/bits"
import { LogViewer } from "@/mock/components/log-viewer"
import { LoomThread, PhaseRuler } from "@/mock/components/loom"
import { Page } from "@/components/page"
import { TopBar } from "@/mock/components/top-bar"
import {
  organization,
  type PlanChange,
  person,
  services,
  stepGroups,
} from "@/mock/data"
import { dateTime, duration, relative, signedMoney } from "@/lib/format"
import { logs } from "@/mock/logs"
import { navigate, to } from "@/mock/router"
import { type DeploymentView, type StepView, useStore } from "@/mock/store"

const triggerText: Record<DeploymentView["trigger"], string> = {
  push: "pushed",
  manual: "deployed from the dashboard",
  cli: "deployed with the CLI",
  api: "deployed through the API",
}

export function DeploymentPage({ id }: { id: string }) {
  const { deployments, now } = useStore()
  const d = deployments.find((x) => x.id === id)
  const lines = useMemo(() => (d ? logs(d, now) : []), [d, now])
  const [tab, setTab] = useState<string | null>(null)
  const [step, setStep] = useState<number | null>(null)

  if (!d) {
    return (
      <>
        <TopBar
          crumbs={[
            { label: "Deployments", path: "/deployments" },
            { label: id },
          ]}
        />
        <Page>
          <Empty className="border border-dashed">
            <EmptyHeader>
              <EmptyTitle>No deployment called {id}</EmptyTitle>
              <EmptyDescription>
                It may belong to another organization, or the link is wrong.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <a href={to("/deployments")} className={buttonVariants()}>
                All deployments
              </a>
            </EmptyContent>
          </Empty>
        </Page>
      </>
    )
  }

  const live = d.status === "running" || d.status === "waiting"
  const activeTab = tab ?? (d.status === "waiting" ? "plan" : "logs")

  return (
    <>
      <TopBar
        crumbs={[
          { label: organization.name, path: "/" },
          { label: "Deployments", path: "/deployments" },
          { label: d.project, path: `/projects/${d.project}` },
          { label: <span className="font-mono text-xs">{d.id}</span> },
        ]}
      />
      <Page className="gap-6">
        <Heading d={d} now={now} />
        <Progression d={d} now={now} />
        {d.status === "waiting" && <ApprovalAlert d={d} />}
        <IdStrip d={d} now={now} />
        <div className="grid items-start gap-6 lg:grid-cols-[300px_minmax(0,1fr)]">
          <Steps
            d={d}
            now={now}
            selected={step}
            onSelect={(i) => {
              setStep((cur) => (cur === i ? null : i))
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
                  {d.changes.length}
                </Badge>
              </TabsTrigger>
              <TabsTrigger value="services">Services</TabsTrigger>
            </TabsList>
            <TabsContent value="logs">
              <LogViewer
                lines={lines}
                live={live}
                name={d.id}
                step={step}
                onClearStep={() => setStep(null)}
              />
            </TabsContent>
            <TabsContent value="plan">
              <Plan d={d} />
            </TabsContent>
            <TabsContent value="services">
              <Services d={d} />
            </TabsContent>
          </Tabs>
        </div>
      </Page>
    </>
  )
}

function Heading({ d, now }: { d: DeploymentView; now: number }) {
  const { redeploy, cancel } = useStore()
  const [confirmCancel, setConfirmCancel] = useState(false)
  const cli = `loomplane deploy ${d.project} --env ${d.env} --ref ${d.sha}`
  const author = person(d.author)

  return (
    <header className="flex flex-col gap-5 md:flex-row md:items-start md:justify-between">
      <div className="grid min-w-0 gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status={d.status} />
          <a
            href={to(`/projects/${d.project}`)}
            className={cn(
              buttonVariants({ variant: "link", size: "sm" }),
              "h-auto px-1 text-foreground"
            )}
          >
            {d.project}
          </a>
          <EnvBadge env={d.env} />
        </div>
        <h1 className="text-[28px] leading-tight font-semibold tracking-[-0.025em] text-balance md:text-[32px]">
          {d.message}
        </h1>
        <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-sm text-muted-foreground">
          <span className="flex items-center gap-2">
            <PersonAvatar id={d.author} />
            <span>
              <span className="text-foreground">{author.name}</span>{" "}
              {triggerText[d.trigger]}
            </span>
          </span>
          <span className="flex items-center gap-1.5">
            <Code className="rounded-md bg-muted px-1.5 py-0.5 text-[12px] text-foreground">
              {d.sha}
            </Code>
            <GitBranchIcon className="size-3.5" />
            {d.branch}
          </span>
          <span title={dateTime(d.startedAt)}>
            Started {relative(d.startedAt, now)}
          </span>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {d.status === "running" && (
          <Button
            variant="outline"
            className="rounded-full"
            onClick={() => setConfirmCancel(true)}
          >
            Cancel
          </Button>
        )}
        {!(d.status === "running" || d.status === "waiting") && (
          <Button
            variant={d.status === "failed" ? "default" : "outline"}
            className="rounded-full"
            onClick={() => {
              const next = redeploy(d.id)
              navigate(`/deployments/${next}`)
              toast.success(`Redeploying ${d.project} to ${d.env}`)
            }}
          >
            <RotateCcwIcon />
            {d.status === "failed" ? "Retry" : "Redeploy"}
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
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard?.writeText(cli)
                toast.success("Copied the CLI command")
              }}
            >
              Copy as CLI command
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard?.writeText(
                  `curl -H "Authorization: Bearer $LOOMPLANE_TOKEN" https://loomplane.dev/api/v1/deployments/${d.id}`
                )
                toast.success("Copied the API request")
              }}
            >
              Copy as API request
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() =>
                toast("The Dispatch run view is part of the full app")
              }
            >
              Open Dispatch run
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <AlertDialog open={confirmCancel} onOpenChange={setConfirmCancel}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Cancel this deployment?</AlertDialogTitle>
            <AlertDialogDescription>
              Loomplane stops after the current step. Resources it already
              created stay, and services keep running the previous release until
              you deploy again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep deploying</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                cancel(d.id)
                setConfirmCancel(false)
                toast(`Canceled the ${d.project} deployment`)
              }}
            >
              Cancel deployment
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </header>
  )
}

/** Progression is the deployment's thread across the warp, phases below. */
function Progression({ d, now }: { d: DeploymentView; now: number }) {
  const step = d.steps[d.current] as StepView
  const left =
    d.status === "running"
      ? d.steps
          .filter((s) => s.index > d.current)
          .reduce((a, s) => a + (d.durations[s.index] ?? 0) * 1000, 0) +
        (d.durations[d.current] ?? 0) * 1000 * (1 - (step.progress ?? 0))
      : 0
  // Where the thread is: mid-step while running, the step it stopped on
  // otherwise, the far end once it's live.
  const glowAt =
    d.status === "succeeded"
      ? 92
      : Math.min(
          ((d.current + (d.status === "running" ? (step.progress ?? 0) : 0.5)) /
            d.steps.length) *
            100,
          96
        )
  const headline =
    d.status === "running"
      ? step.step.name
      : d.status === "waiting"
        ? "Waiting for approval"
        : d.status === "failed"
          ? `Failed at ${step.step.name.toLowerCase()}`
          : d.status === "canceled"
            ? `Canceled during ${step.step.name.toLowerCase()}`
            : "Live"
  return (
    <Card className="relative overflow-hidden">
      {d.status !== "canceled" && (
        <div
          aria-hidden="true"
          data-tone={
            d.status === "failed"
              ? "failed"
              : d.status === "succeeded"
                ? "quiet"
                : undefined
          }
          className="loom-glow pointer-events-none absolute inset-0"
          style={{ "--glow-x": `${glowAt}%` } as CSSProperties}
        />
      )}
      <CardContent className="relative grid gap-4 py-2">
        <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1">
          <div className="flex items-baseline gap-2">
            <span className="text-lg font-semibold tracking-tight">
              {headline}
            </span>
            <span className="text-sm text-muted-foreground">
              {d.status === "succeeded"
                ? `all ${d.steps.length} steps`
                : `step ${d.current + 1} of ${d.steps.length}`}
            </span>
          </div>
          <div className="flex items-baseline gap-4 text-sm text-muted-foreground tabular-nums">
            <span>
              <span className="font-medium text-foreground">
                {duration(d.elapsedMs)}
              </span>{" "}
              elapsed
            </span>
            {d.status === "running" && <span>about {duration(left)} left</span>}
            {d.finishedAt && d.status !== "running" && (
              <span>finished {relative(d.finishedAt, now)}</span>
            )}
          </div>
        </div>
        <LoomThread steps={d.steps} size="lg" tooltips />
        <PhaseRuler
          steps={d.steps}
          active={d.status === "succeeded" ? undefined : step.step.group}
        />
      </CardContent>
    </Card>
  )
}

function IdStrip({ d, now }: { d: DeploymentView; now: number }) {
  const { projects } = useStore()
  const proj = projects.find((p) => p.id === d.project)
  const items: { label: string; value: ReactNode; copy?: string }[] = [
    { label: "Deployment", value: <Code>{d.id}</Code>, copy: d.id },
    {
      label: "Dispatch run",
      value: <Code>{d.dispatchRun}</Code>,
      copy: d.dispatchRun,
    },
    {
      label: "Ctrl Plane release",
      value: <Code>{d.ctrlPlaneRelease}</Code>,
      copy: d.ctrlPlaneRelease,
    },
    { label: "Manifest", value: `revision ${proj?.manifestRevision ?? 1}` },
    {
      label: "Approval",
      value: d.requiresApproval
        ? d.approvedAt
          ? `${person(d.approvedBy ?? "maya").name.split(" ")[0]}, ${relative(d.approvedAt, now)}`
          : d.canceledAt
            ? "Rejected"
            : "Waiting"
        : "Not needed",
    },
  ]
  return (
    <dl className="grid grid-cols-2 border-y sm:grid-cols-3 lg:grid-cols-5">
      {items.map((it, i) => (
        <div
          key={it.label}
          className={cn(
            "grid gap-1 py-3.5 pr-4",
            i > 0 && "lg:border-l lg:pl-5"
          )}
        >
          <dt className="text-xs text-muted-foreground">{it.label}</dt>
          <dd className="flex min-w-0 items-center gap-1 text-sm">
            <span className="truncate">{it.value}</span>
            {it.copy && (
              <CopyButton
                value={it.copy}
                label={`Copy ${it.label.toLowerCase()} ID`}
              />
            )}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function changeCounts(changes: PlanChange[]) {
  return {
    create: changes.filter((c) => c.action === "create").length,
    update: changes.filter((c) => c.action === "update").length,
    destroy: changes.filter((c) => c.action === "destroy").length,
    cost: changes.reduce((a, c) => a + c.cost, 0),
  }
}

function ApprovalAlert({ d }: { d: DeploymentView }) {
  const { approve, cancel } = useStore()
  const c = changeCounts(d.changes)
  const [rejecting, setRejecting] = useState(false)
  const why =
    c.destroy > 0
      ? `This plan removes ${c.destroy} ${c.destroy === 1 ? "resource" : "resources"}, so an admin has to approve it.`
      : "Production deploys wait for an admin to approve the plan."
  return (
    <Alert className="items-center gap-y-1 rounded-xl border-weld/50 bg-[color-mix(in_oklch,var(--weld)_6%,var(--card))] p-4 has-[>svg]:gap-x-3 md:has-[>svg]:grid-cols-[auto_1fr_auto]">
      <ShieldAlertIcon className="text-weld-ink size-5!" />
      <AlertTitle>{why}</AlertTitle>
      <AlertDescription className="col-start-2">
        {c.create} to create, {c.update} to change, {c.destroy} to remove.
        Monthly cost {signedMoney(c.cost)}. Nothing has changed in your clouds
        yet.
      </AlertDescription>
      <div className="col-start-2 mt-2 flex gap-2 md:col-start-3 md:row-span-2 md:row-start-1 md:mt-0">
        <Button
          variant="outline"
          className="rounded-full"
          onClick={() => setRejecting(true)}
        >
          Reject
        </Button>
        <Button
          className="rounded-full"
          onClick={() => {
            approve(d.id)
            toast.success("Plan approved", {
              description: `Deploying ${d.project} to ${d.env}.`,
            })
          }}
        >
          <CheckIcon />
          Approve and deploy
        </Button>
      </div>
      <AlertDialog open={rejecting} onOpenChange={setRejecting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Reject this plan?</AlertDialogTitle>
            <AlertDialogDescription>
              The deployment stops here and nothing changes in your clouds.{" "}
              {person(d.author).name} can push a new commit to plan again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep reviewing</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                cancel(d.id)
                setRejecting(false)
                toast("Plan rejected")
              }}
            >
              Reject plan
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Alert>
  )
}

function StepIcon({ s }: { s: StepView }) {
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
  d,
  now,
  selected,
  onSelect,
}: {
  d: DeploymentView
  now: number
  selected: number | null
  onSelect: (index: number) => void
}) {
  return (
    <Card className="gap-2 lg:sticky lg:top-16">
      <CardHeader>
        <CardTitle className="text-sm">Steps</CardTitle>
        <CardDescription className="text-xs">
          Pick one to filter the logs
        </CardDescription>
      </CardHeader>
      <ol className="grid px-2">
        {stepGroups.map((g) => (
          <li key={g} className="grid">
            <span className="px-2 pt-3 pb-1 text-xs text-muted-foreground">
              {g}
            </span>
            <ol className="relative grid">
              <span
                aria-hidden="true"
                className="absolute top-4 bottom-4 left-[19px] w-px bg-border"
              />
              {d.steps
                .filter((s) => s.step.group === g)
                .map((s) => {
                  const reached = s.state !== "pending" && s.state !== "skipped"
                  const time =
                    s.step.key === "approval" &&
                    !d.requiresApproval &&
                    s.state === "done"
                      ? "Not needed"
                      : s.durationMs !== undefined
                        ? duration(s.durationMs)
                        : (s.state === "running" || s.state === "waiting") &&
                            s.startedAt
                          ? duration(now - s.startedAt)
                          : ""
                  return (
                    <li key={s.index}>
                      <Button
                        variant="ghost"
                        disabled={!reached}
                        aria-pressed={selected === s.index}
                        onClick={() => onSelect(s.index)}
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
                          {s.step.name}
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

const actionStyle: Record<
  PlanChange["action"],
  { icon: typeof PlusIcon; label: string; tone: string }
> = {
  create: {
    icon: PlusIcon,
    label: "Create",
    tone: "text-overdye bg-overdye/12",
  },
  update: {
    icon: PencilIcon,
    label: "Change",
    tone: "text-muted-foreground bg-muted",
  },
  destroy: {
    icon: MinusIcon,
    label: "Remove",
    tone: "text-destructive bg-destructive/12",
  },
}

function Plan({ d }: { d: DeploymentView }) {
  const c = changeCounts(d.changes)
  return (
    <Card className="gap-0 py-0">
      <CardHeader className="flex flex-wrap items-center gap-x-5 gap-y-2 border-b py-4">
        {(["create", "update", "destroy"] as const).map((a) => {
          const Icon = actionStyle[a].icon
          return (
            <span key={a} className="flex items-center gap-2 text-sm">
              <span
                className={cn(
                  "flex size-5 items-center justify-center rounded-md",
                  actionStyle[a].tone
                )}
              >
                <Icon className="size-3" strokeWidth={2.5} />
              </span>
              <span className="tabular-nums">{c[a]}</span>
              <span className="text-muted-foreground">
                to {actionStyle[a].label.toLowerCase()}
              </span>
            </span>
          )
        })}
        <span className="ml-auto text-sm text-muted-foreground">
          Monthly cost{" "}
          <span className="font-medium text-foreground tabular-nums">
            {signedMoney(c.cost)}
          </span>
        </span>
      </CardHeader>
      <div className="divide-y">
        {d.changes.map((ch) => {
          const style = actionStyle[ch.action]
          const Icon = style.icon
          return (
            <Collapsible
              key={`${ch.kind}-${ch.name}`}
              defaultOpen={ch.action !== "update"}
            >
              <CollapsibleTrigger className="group/change flex w-full items-center gap-3 px-5 py-3.5 text-left text-sm outline-none hover:bg-muted/50 focus-visible:bg-muted/50">
                <span
                  className={cn(
                    "flex size-5 shrink-0 items-center justify-center rounded-md",
                    style.tone
                  )}
                >
                  <Icon className="size-3" strokeWidth={2.5} />
                  <span className="sr-only">{style.label}</span>
                </span>
                <ProviderGlyph id={ch.provider} />
                <span className="min-w-0 flex-1 truncate">
                  <span className="text-muted-foreground">{ch.kind}</span>{" "}
                  <span className="font-mono font-medium">{ch.name}</span>
                </span>
                <span
                  className={cn(
                    "text-xs tabular-nums",
                    ch.cost === 0 ? "text-muted-foreground" : "text-foreground"
                  )}
                >
                  {signedMoney(ch.cost)}/mo
                </span>
                <ChevronRightIcon className="size-3.5 text-muted-foreground transition-transform group-data-[panel-open]/change:rotate-90" />
              </CollapsibleTrigger>
              <CollapsibleContent>
                <dl className="mx-5 mb-4 grid grid-cols-[minmax(8rem,auto)_1fr] gap-x-6 gap-y-1.5 rounded-lg bg-(--terminal) p-4 font-mono text-[12px] text-(--terminal-foreground)">
                  {ch.attrs.map((a) => (
                    <div key={a.key} className="contents">
                      <dt className="text-(--terminal-muted)">{a.key}</dt>
                      <dd className="flex flex-wrap items-center gap-x-2">
                        {a.before !== undefined && (
                          <span
                            className={cn(
                              "text-[color-mix(in_oklch,var(--destructive)_60%,white)]",
                              a.after !== undefined && "line-through"
                            )}
                          >
                            {a.after !== undefined ? "" : "- "}
                            {a.before}
                          </span>
                        )}
                        {a.after !== undefined && (
                          <span className="text-[color-mix(in_oklch,var(--overdye)_55%,white)]">
                            + {a.after}
                          </span>
                        )}
                      </dd>
                    </div>
                  ))}
                </dl>
              </CollapsibleContent>
            </Collapsible>
          )
        })}
      </div>
    </Card>
  )
}

function Services({ d }: { d: DeploymentView }) {
  const svc = services.filter((s) => s.project === d.project)
  const deployStep = d.steps.find((s) => s.step.key === "deploy")
  return (
    <Card className="py-2">
      <ItemGroup className="gap-0">
        {svc.map((s, i) => {
          const share = (deployStep?.progress ?? 0) * svc.length
          // A failure after rollout means Loomplane rolled the release back.
          const rolledBack =
            d.status === "failed" && deployStep?.state === "done"
          const state = rolledBack
            ? "Rolled back"
            : deployStep?.state === "done"
              ? "Released"
              : deployStep?.state === "running"
                ? share > i + 1
                  ? "Released"
                  : share > i
                    ? "Rolling out"
                    : "Queued"
                : deployStep?.state === "failed"
                  ? "Rolled back"
                  : "Not started"
          return (
            <div key={s.id}>
              {i > 0 && <ItemSeparator className="my-0" />}
              <Item className="rounded-none px-5">
                <ItemMedia>
                  <ProviderGlyph id={s.provider} />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle>{s.name}</ItemTitle>
                  <ItemDescription className="line-clamp-1 text-xs">
                    {s.framework} on {s.runtime}, {s.region}
                  </ItemDescription>
                </ItemContent>
                <ItemActions
                  className={cn(
                    "gap-1.5 text-xs",
                    state === "Rolling out"
                      ? "text-weld-ink font-medium"
                      : "text-muted-foreground"
                  )}
                >
                  <span
                    className={cn(
                      "size-1.5 rounded-full",
                      state === "Released" && "bg-overdye",
                      state === "Rolling out" && "animate-pulse bg-weld",
                      state === "Rolled back" && "bg-destructive",
                      (state === "Queued" || state === "Not started") &&
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
