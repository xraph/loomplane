// One release preparation: what would change in production and whether the
// agent may promote it alone. The gates and their details are Loomplane's
// own words, computed from evidence. Text that came from outside Loomplane
// (commit titles, migration patches, report titles, what the agent wrote)
// is shown as plain text, and the agent's concerns as quotes.
import {
  CheckIcon,
  DatabaseIcon,
  HourglassIcon,
  QuoteIcon,
  XIcon,
} from "lucide-react"
import type { ReactNode } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useAgentRequestsGet,
  useProjectsList,
  useReleaseReportsCancel,
  useReleaseReportsGet,
  useReleaseReportsList,
} from "@/client/forge/loomplane/hooks"
import type {
  ReadinessEvidence,
  ReleaseReport,
  SourceDiff,
} from "@/client/forge/loomplane/types"
import {
  useInvalidate,
  useMutation,
  useQuery,
  useRefetchWhile,
} from "@/client/hooks"
import { AgentRequestCard } from "@/components/agent-request-card"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { artifactLine } from "@/components/release-plan"
import { StatusPill, toneClass, type Tone } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useAgent, useFeatures } from "@/lib/features"
import { duration } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useProjectCan } from "@/lib/project-permissions"
import {
  gateNames,
  isActive,
  mayCancelReport,
  stateLabel,
  stateTone,
  useReleaseReportEvents,
  type StateTone,
} from "@/lib/release-reports"
import { ago } from "@/lib/relative-time"
import { Link } from "@/lib/router"
import { isAgentActor, useNow, usePeople } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

const toneOf: Record<StateTone, Tone> = {
  neutral: "quiet",
  progress: "flight",
  success: "live",
  warning: "waiting",
  danger: "failed",
}

/** ReleaseReportPage is the page of one release report. */
export function ReleaseReportPage({ id }: { id: string }) {
  const features = useFeatures()
  return (
    <>
      <SiteHeader crumbs={[{ label: "Release report" }]} />
      <Page className="max-w-[880px]">
        {features.ai ? <WithAgent id={id} /> : null}
      </Page>
    </>
  )
}

// WithAgent reads the report only while the workspace has the agent on:
// the server refuses it otherwise.
function WithAgent({ id }: { id: string }) {
  const agent = useAgent()
  if (agent.loading) return <Skeleton className="h-64 w-full rounded-[14px]" />
  if (!agent.enabled) return <AgentOff />
  return <Loaded id={id} />
}

function AgentOff() {
  return (
    <p className="max-w-[64ch] text-[13px] text-muted-foreground">
      The agent is off for this workspace, so its release reports can&apos;t be
      read. An admin can turn it on in AI settings.
    </p>
  )
}

function Loaded({ id }: { id: string }) {
  useReleaseReportEvents()
  const report = useQuery(useReleaseReportsGet, { path: { id } })
  // Without a live connection the report still catches up while it's active.
  useRefetchWhile(
    !!report.data && isActive(report.data.state),
    report.refetch,
    5000
  )
  // The agent turned off since its status was read.
  if (report.status === "error" && statusOf(report.error) === 409)
    return <AgentOff />
  if (report.status === "error")
    return (
      <QueryError
        error={report.error}
        fallback="The release report couldn't be loaded."
        retry={report.refetch}
      />
    )
  if (!report.data) return <Skeleton className="h-64 w-full rounded-[14px]" />
  return <Detail report={report.data} />
}

function Detail({ report: r }: { report: ReleaseReport }) {
  const can = useProjectCan(r.projectId)
  const { session } = useOrganization()
  const projects = useQuery(useProjectsList)
  const project = projects.data?.projects.find((p) => p.id === r.projectId)
  const person = usePeople()
  const now = useNow(30_000)
  const mayCancel = mayCancelReport(r, {
    userId: session.userId,
    can,
  })
  const ev = r.evidence
  return (
    <>
      <PageHeading
        title={`${r.fromEnvironment} to ${r.toEnvironment}`}
        description={[
          project?.name,
          `Asked by ${person(r.requestedBy).name} ${ago(r.createdAt, now)}`,
        ]
          .filter(Boolean)
          .join(" · ")}
        actions={mayCancel && <CancelReport report={r} />}
      />
      <div className="grid gap-1.5">
        <p className="flex flex-wrap items-center gap-2">
          <StatusPill tone={toneOf[stateTone(r.state)]}>
            {stateLabel(r.state)}
          </StatusPill>
          {r.preview && <Badge variant="outline">Preview</Badge>}
        </p>
        {r.stateReason && (
          <p className="max-w-[72ch] text-[13px] text-pretty">
            {r.stateReason}
          </p>
        )}
        {r.error && (
          <Alert variant="destructive">
            <AlertDescription>{r.error}</AlertDescription>
          </Alert>
        )}
      </div>

      <Decision report={r} />
      <Gates report={r} />
      {r.state === "waiting_for_person" && <NeedsAPerson report={r} />}
      {ev && <Evidence report={r} evidence={ev} />}
      <FromTheAgent report={r} />
    </>
  )
}

function Section({
  title,
  note,
  children,
}: {
  title: string
  note?: ReactNode
  children: ReactNode
}) {
  return (
    <Panel aria-label={title} className="gap-3">
      <div className="grid gap-0.5">
        <h2 className="text-[14px] font-semibold">{title}</h2>
        {note && (
          <p className="max-w-[72ch] text-[12.5px] leading-normal text-muted-foreground">
            {note}
          </p>
        )}
      </div>
      {children}
    </Panel>
  )
}

/** Decision says who decided, or who decides, and the two rules that always apply. */
function Decision({ report: r }: { report: ReleaseReport }) {
  const person = usePeople()
  const who = r.decidedBy
    ? isAgentActor(r.decidedBy)
      ? "Loomplane agent"
      : person(r.decidedBy).name
    : ""
  return (
    <Section title="Decision">
      <p className="text-[13px]">
        {who
          ? `Decided by ${who}.`
          : r.decision === "promote"
            ? "Every gate held, so the agent may promote it."
            : r.decision === "ask"
              ? "A person decides."
              : "Not decided yet."}
      </p>
      <ul className="grid max-w-[72ch] gap-1 text-[12.5px] leading-normal text-muted-foreground">
        {r.via === "chat" && (
          <li className="text-foreground">
            This release was prepared from chat, so a person decides.
          </li>
        )}
        {r.reusedPlan && (
          <li className="text-foreground">
            This release reuses a plan someone else made, so a person decides.
          </li>
        )}
        <li>
          A release prepared from chat, or one reusing someone else&apos;s plan,
          always waits for a person.
        </li>
        <li>
          A source&apos;s first production release always goes to a person:
          there&apos;s nothing in production to compare it to.
        </li>
        <li>
          When autonomous promotion is off for the project, the agent still asks
          before production.
        </li>
      </ul>
    </Section>
  )
}

function Gates({ report: r }: { report: ReleaseReport }) {
  return (
    <Section
      title="The seven gates"
      note={
        r.preview
          ? "A preview: Loomplane worked these out while preparing the release. They are checked again when the agent asks to promote."
          : "Loomplane works these out from evidence. The agent can add reasons to ask, and can't clear a gate."
      }
    >
      {r.gates.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          No gate has been checked yet.
        </p>
      ) : (
        <ul className="grid gap-2">
          {r.gates.map((g) => (
            <li key={g.number} className="flex items-start gap-2.5">
              <span
                className={cn(
                  "mt-0.5 inline-flex size-5 shrink-0 items-center justify-center rounded-full",
                  g.passed ? toneClass.live.tint : toneClass.failed.tint,
                  g.passed ? toneClass.live.ink : toneClass.failed.ink
                )}
              >
                {g.passed ? (
                  <CheckIcon className="size-3" aria-hidden="true" />
                ) : (
                  <XIcon className="size-3" aria-hidden="true" />
                )}
              </span>
              <div className="grid min-w-0 gap-0.5 text-[13px]">
                <span className="font-medium">
                  Gate {g.number}: {g.name || gateNames[g.number]}
                  <span className="sr-only">
                    {g.passed ? ", passed" : ", failed"}
                  </span>
                </span>
                <span className="text-muted-foreground">{g.detail}</span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function NeedsAPerson({ report: r }: { report: ReleaseReport }) {
  const failed = r.gates.filter((g) => !g.passed)
  const request = useQuery(useAgentRequestsGet, {
    path: { id: r.requestId },
  })
  return (
    <Section
      title="Needs a person"
      note="The plan waits for someone who can approve deployments in this project."
    >
      {failed.length > 0 ? (
        <ul className="ml-4 list-disc text-[13px]">
          {failed.map((g) => (
            <li key={g.number}>
              Gate {g.number}, {g.name || gateNames[g.number]}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[13px]">
          No gate failed, but this release always waits for a person.
        </p>
      )}
      {r.requestId && request.data ? (
        <AgentRequestCard request={request.data} />
      ) : r.planId ? (
        <p className="flex items-center gap-1.5 text-[13px]">
          <HourglassIcon className="size-3.5 text-muted-foreground" />
          <Link
            to="/deployments/$id"
            params={{ id: r.planId }}
            className="text-primary underline-offset-2 hover:underline"
          >
            Open the plan
          </Link>
        </p>
      ) : null}
    </Section>
  )
}

function Evidence({
  report: r,
  evidence: ev,
}: {
  report: ReleaseReport
  evidence: ReadinessEvidence
}) {
  const unavailable = Object.entries(ev.unavailable ?? {})
  return (
    <>
      {ev.sources.map((s) => (
        <Source key={s.sourceId || s.alias} source={s} />
      ))}
      {ev.artifacts.length > 0 && (
        <Section title="Images">
          <ul className="grid gap-1 text-[13px]">
            {ev.artifacts.map((a) => (
              <li key={a.service}>
                <span className="font-medium">{a.service}</span>
                <span className="text-muted-foreground">
                  {": "}
                  {artifactLine(a)}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
      <Staging report={r} evidence={ev} />
      <Production evidence={ev} />
      {ev.openReports.length > 0 && (
        <Section title="Open on staging">
          <ul className="grid gap-1.5 text-[13px]">
            {ev.openReports.map((o) => (
              <li key={o.id} className="flex flex-wrap items-center gap-2">
                {o.severity && <Badge variant="outline">{o.severity}</Badge>}
                <Link
                  to={o.path as "/"}
                  className="text-primary underline-offset-2 hover:underline"
                >
                  {o.title}
                </Link>
              </li>
            ))}
          </ul>
        </Section>
      )}
      {ev.budget && (
        <Section title="Budget">
          <p className="text-[13px]">
            {ev.budget.exhausted
              ? "The agent's budget is spent."
              : `The agent has spent ${ev.budget.spentUsd} of ${ev.budget.limitUsd} USD.`}
          </p>
        </Section>
      )}
      {unavailable.length > 0 && (
        <Section
          title="Evidence Loomplane couldn't read"
          note="A gate that needs evidence it can't read fails, so the release asks a person."
        >
          <ul className="grid gap-1 text-[13px]">
            {unavailable.map(([key, why]) => (
              <li key={key}>
                <span className="font-medium">{key}</span>
                <span className="text-muted-foreground">: {String(why)}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </>
  )
}

function Source({ source: s }: { source: SourceDiff }) {
  const direction: Record<string, string> = {
    ahead: "Staging is ahead of production",
    behind: "Staging is behind production",
    diverged: "Staging and production have diverged",
    identical: "Staging and production run the same commit",
    new: "Production has never run this source",
  }
  return (
    <Section title={s.alias}>
      <p className="text-[13px]">
        {direction[s.direction] ?? s.direction}
        <span className="text-muted-foreground">
          {" · "}
          {s.totalCommits} {s.totalCommits === 1 ? "commit" : "commits"},{" "}
          {s.filesChanged} {s.filesChanged === 1 ? "file" : "files"}
          {s.productionCommit &&
            ` · production ${s.productionCommit.slice(0, 7)}`}
          {s.stagingCommit && ` · staging ${s.stagingCommit.slice(0, 7)}`}
        </span>
      </p>
      {s.direction === "new" ? (
        <Alert>
          <AlertDescription>
            Production has never run this source, so there&apos;s nothing to
            compare it to. Its first production release always goes to a person.
          </AlertDescription>
        </Alert>
      ) : (
        (s.truncated || s.error) && (
          <Alert>
            <AlertDescription>
              This diff wasn&apos;t read in full, so the release asks a person.
              {s.error && <> The source said: {s.error}</>}
            </AlertDescription>
          </Alert>
        )
      )}
      {s.migrations.length > 0 && (
        <div className="grid gap-2">
          <p className="flex items-center gap-1.5 text-[13px] font-medium">
            <DatabaseIcon className="size-3.5" aria-hidden="true" />
            Database migrations
          </p>
          {s.migrations.map((m) => (
            <details
              key={m.path}
              className={cn(
                "rounded-lg px-3 py-2 text-[13px] ring-1 ring-border",
                toneClass.waiting.tint
              )}
            >
              <summary className="cursor-pointer break-all">
                <span className="font-mono text-[12px]">{m.path}</span>
                <span className="text-muted-foreground">
                  {" · "}
                  {m.status}, +{m.additions} −{m.deletions}
                </span>
              </summary>
              {m.patch ? (
                <pre className="mt-2 max-h-72 overflow-auto font-mono text-[12px] leading-snug whitespace-pre-wrap">
                  {m.patch}
                </pre>
              ) : (
                <p className="mt-2 text-muted-foreground">
                  The patch wasn&apos;t kept.
                </p>
              )}
              {m.truncated && (
                <p className="mt-1 text-muted-foreground">
                  The patch is cut short.
                </p>
              )}
            </details>
          ))}
        </div>
      )}
      {s.commits.length > 0 && (
        <ul className="grid gap-1 text-[13px]">
          {s.commits.map((c) => (
            <li key={c.sha} className="flex min-w-0 gap-2">
              <span className="shrink-0 font-mono text-[12px] text-muted-foreground">
                {c.sha.slice(0, 7)}
              </span>
              <span className="min-w-0 break-words">
                {c.title}
                {c.author && (
                  <span className="text-muted-foreground"> · {c.author}</span>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function Staging({
  report: r,
  evidence: ev,
}: {
  report: ReleaseReport
  evidence: ReadinessEvidence
}) {
  const soak = ev.soak
  return (
    <Section title={`${r.fromEnvironment} soak`}>
      {!soak ? (
        <p className="text-[13px] text-muted-foreground">
          How long {r.fromEnvironment} has run this wasn&apos;t read.
        </p>
      ) : !soak.running ? (
        <p className="text-[13px]">
          {soak.environment} doesn&apos;t run all of these commits yet.
        </p>
      ) : (
        <p className="flex flex-wrap items-center gap-2 text-[13px]">
          <span>
            {soak.environment} has run them for{" "}
            {duration(Math.round(soak.duration / 1e6 / 60_000) * 60_000)},{" "}
            {soak.unverified
              ? "with too little evidence to judge"
              : soak.healthy
                ? "healthy"
                : "not healthy"}
            .
          </span>
          {soak.unverified && <Badge variant="outline">Unverified</Badge>}
        </p>
      )}
      {!ev.pinsMatch && ev.pinDifferences.length > 0 && (
        <ul className="ml-4 list-disc text-[13px]">
          {ev.pinDifferences.map((d) => (
            <li key={d}>{d}</li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function Production({ evidence: ev }: { evidence: ReadinessEvidence }) {
  const cov = ev.coverage
  const canaryOf = new Map(ev.canaries.map((c) => [c.deploymentId, c]))
  return (
    <Section title="Production canaries and watch">
      {ev.changedServices.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          The plan changes no service.
        </p>
      ) : (
        <ul className="grid gap-1.5 text-[13px]">
          {ev.changedServices.map((s) => {
            const last = s.lastCanary ? canaryOf.get(s.lastCanary) : undefined
            return (
              <li key={s.actionId} className="flex flex-wrap gap-x-2">
                <span className="font-medium">{s.name}</span>
                <span className="text-muted-foreground">
                  {s.op}
                  {s.provider && `, ${s.provider}`}
                  {" · "}
                  {last ? (
                    <>
                      last canary {last.status.replace(/_/g, " ")} in{" "}
                      <Link
                        to="/deployments/$id"
                        params={{ id: last.deploymentId }}
                        className="underline underline-offset-2"
                      >
                        {last.deploymentId}
                      </Link>
                    </>
                  ) : (
                    "No canary ran"
                  )}
                </span>
              </li>
            )
          })}
        </ul>
      )}
      {cov && (
        <div className="grid gap-1 text-[13px]">
          <p>
            {cov.duration > 0
              ? `A release in ${cov.environment} would be watched for ${duration(Math.round(cov.duration / 1e6 / 60_000) * 60_000)}.`
              : `No watch would be armed in ${cov.environment}.`}
            {cov.canary.length > 0 && (
              <> A canary ramps {cov.canary.join(", ")}.</>
            )}
          </p>
          {cov.services.length > 0 && (
            <ul className="grid gap-0.5 text-muted-foreground">
              {cov.services.map((s) => (
                <li key={s.name}>
                  {s.name}:{" "}
                  {s.watchable
                    ? "No canary ran; a watch guards it"
                    : `not watched${s.reason ? `, ${s.reason}` : ""}`}
                </li>
              ))}
            </ul>
          )}
          {cov.unverified && (
            <p className="text-muted-foreground">
              A release here now would leave evidence missing.
            </p>
          )}
        </div>
      )}
    </Section>
  )
}

function FromTheAgent({ report: r }: { report: ReleaseReport }) {
  if (r.concerns.length === 0) return null
  return (
    <Section
      title="From the agent"
      note="The agent's own words, not Loomplane's. A concern can only make a release more cautious."
    >
      <ul className="grid gap-2.5">
        {r.concerns.map((c, i) => (
          <li key={i} className="grid gap-1">
            <Badge
              variant="outline"
              className={cn(c.kind === "ask" && toneClass.waiting.ink)}
            >
              {c.kind === "ask" ? "Asked for a person" : "Risk"}
            </Badge>
            <blockquote className="flex gap-2 border-l-2 border-border pl-3 text-[13px] break-words whitespace-pre-wrap">
              <QuoteIcon
                className="mt-0.5 size-3 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <span>{c.text}</span>
            </blockquote>
          </li>
        ))}
      </ul>
    </Section>
  )
}

function CancelReport({ report: r }: { report: ReleaseReport }) {
  const cancel = useMutation(useReleaseReportsCancel)
  const invalidate = useInvalidate()
  const run = async () => {
    try {
      await cancel.mutateAsync({ path: { id: r.id } })
      invalidate(useReleaseReportsGet)
      invalidate(useReleaseReportsList)
      toast.success(
        "Release cancelled. The plan, if there is one, waits for a person."
      )
    } catch (err) {
      toast.error(messageOf(err, "The release couldn't be cancelled."))
    }
  }
  return (
    <Button
      variant="outline"
      disabled={cancel.isPending}
      onClick={() => void run()}
    >
      {cancel.isPending && <Spinner data-icon="inline-start" />}
      Cancel
    </Button>
  )
}
