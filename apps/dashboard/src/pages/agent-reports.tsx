// What the agent saw go wrong in the workspace: one row per incident, with
// how bad it is, whether the agent looked into it and where its issue is.
// Open reports show first, and a banner says when the agent stopped
// investigating because the workspace used its AI budget.
import { SparklesIcon } from "lucide-react"
import { useState } from "react"

import {
  useAgentReportsList,
  useAgentWatcherGet,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Issue, Report } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { useAgent, useFeatures } from "@/lib/features"
import { useOrganization } from "@/lib/organization"
import { ago } from "@/lib/relative-time"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"
import { useNow } from "@/lib/use-runs"

const statuses = [
  ["open", "Open"],
  ["resolved", "Resolved"],
  ["closed", "Closed"],
  ["all", "All"],
] as const

type Status = (typeof statuses)[number][0]

const severityLook: Record<string, string> = {
  critical: "bg-failed",
  high: "bg-failed",
  medium: "bg-weld",
  low: "bg-muted-foreground",
}

/** SeverityDot is the colored dot beside a report's title. */
export function SeverityDot({ severity }: { severity: string }) {
  return (
    <span
      role="img"
      aria-label={`${severity} severity`}
      className={cn(
        "size-2 shrink-0 rounded-full",
        severityLook[severity] ?? "bg-muted-foreground"
      )}
    />
  )
}

/** investigationText says how far the agent got with a report. */
export function investigationText(r: Report): string {
  switch (r.investigation) {
    case "queued":
    case "running":
      return "Investigating"
    case "done":
      return "Findings ready"
    default:
      return r.investigationNote
        ? `Not investigated: ${r.investigationNote}`
        : "Not investigated"
  }
}

/** issueWhy says why a report has no GitHub issue. */
export function issueWhy(issue: Issue): string {
  if (issue.error) return `Herald only: ${issue.error}`
  switch (issue.state) {
    case "waiting_for_permission":
      return "Herald only: GitHub hasn't accepted the Issues permission yet"
    case "no_repository":
      return "Herald only: Loomplane couldn't tell which repository this is about"
    default:
      return "Herald only"
  }
}

/** IssueLine is the link to a report's issue, or why there isn't one. */
export function IssueLine({ issue }: { issue: Issue }) {
  if (issue.url) {
    return (
      <a
        href={issue.url}
        target="_blank"
        rel="noreferrer noopener"
        className="underline underline-offset-2 hover:text-foreground"
      >
        {issue.number ? `Issue #${issue.number}` : "Issue"}
        {issue.state === "closed" ? " (closed)" : ""}
      </a>
    )
  }
  return <span>{issueWhy(issue)}</span>
}

/** AgentReports is the page of what the agent has reported. */
export function AgentReports() {
  const features = useFeatures()
  const agent = useAgent()
  const { can } = useOrganization()
  return (
    <>
      <SiteHeader crumbs={[{ label: "Agent reports" }]} />
      <Page className="max-w-[880px]">
        <PageHeading
          title="Agent reports"
          description="What the agent saw go wrong in your workspace, what Loomplane already did about it, and where the report went."
        />
        {!features.ai ? null : !can("agent:use") ? (
          <Notice
            title="Agent reports are restricted"
            text="Ask a workspace admin for access to the agent."
          />
        ) : !agent.enabled && !agent.loading ? (
          <Notice
            title="The agent is off"
            text="Reports show up here once a workspace admin turns the agent on in AI settings."
          />
        ) : (
          agent.enabled && <Reports />
        )}
      </Page>
    </>
  )
}

function Notice({ title, text }: { title: string; text: string }) {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <SparklesIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{text}</ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

function Reports() {
  const [status, setStatus] = useState<Status>("open")
  const [project, setProject] = useState("")
  const projects = useQuery(useProjectsList)
  const names = new Map(
    (projects.data?.projects ?? []).map((p) => [p.id, p.name])
  )
  const choices = [
    { label: "All projects", value: "" },
    ...(projects.data?.projects ?? []).map((p) => ({
      label: p.name,
      value: p.id,
    })),
  ]
  return (
    <>
      <Paused />
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div
          role="group"
          aria-label="Report status"
          className="inline-flex rounded-lg bg-muted p-0.5"
        >
          {statuses.map(([value, label]) => (
            <button
              key={value}
              type="button"
              aria-pressed={status === value}
              onClick={() => setStatus(value)}
              className={cn(
                "h-7 rounded-md px-3 text-[13px] font-medium transition-colors",
                status === value
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {label}
            </button>
          ))}
        </div>
        <Select
          items={choices}
          value={project}
          onValueChange={(v) => setProject(v ?? "")}
        >
          <SelectTrigger aria-label="Project" className="w-48">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {choices.map((c) => (
              <SelectItem key={c.value} value={c.value}>
                {c.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <Rows status={status} project={project} names={names} />
    </>
  )
}

/** Paused says so when the workspace's AI budget stopped the investigations. */
function Paused() {
  const watcher = useQuery(useAgentWatcherGet)
  const { can } = useOrganization()
  if (!watcher.data?.paused) return null
  return (
    <Alert>
      <AlertDescription>
        The agent stopped investigating: this workspace has used its AI budget
        for the month. Reports still open and go out as usual.
        {can("ai:manage") && (
          <>
            {" "}
            <Link
              to="/settings/ai"
              className="font-medium underline underline-offset-2"
            >
              Open AI settings
            </Link>
          </>
        )}
      </AlertDescription>
    </Alert>
  )
}

function Rows({
  status,
  project,
  names,
}: {
  status: Status
  project: string
  names: Map<string, string>
}) {
  const now = useNow(30_000)
  const list = useQuery(useAgentReportsList, {
    query: { status, ...(project ? { project_id: project } : {}) },
  })
  if (list.status === "error")
    return (
      <QueryError
        error={list.error}
        fallback="The agent's reports couldn't be loaded."
        retry={list.refetch}
      />
    )
  if (!list.data) return <Skeleton className="h-40 w-full rounded-[14px]" />
  const reports = list.data.reports
    .filter((r): r is Report => r !== null)
    .sort((a, b) => Date.parse(b.lastSeenAt) - Date.parse(a.lastSeenAt))
  if (reports.length === 0)
    return (
      <Notice
        title={status === "open" ? "Nothing is open" : "No reports"}
        text="When the agent sees a deployment fail or a release roll back, the report shows up here."
      />
    )
  return (
    <ul className="grid gap-2">
      {reports.map((r) => (
        <li key={r.id}>
          <ReportRow report={r} project={names.get(r.projectId)} now={now} />
        </li>
      ))}
    </ul>
  )
}

function ReportRow({
  report: r,
  project,
  now,
}: {
  report: Report
  project?: string
  now: number
}) {
  return (
    <article className="grid gap-1.5 rounded-[14px] bg-card px-4 py-3 ring-1 ring-border">
      <div className="flex items-center gap-2">
        <SeverityDot severity={r.severity} />
        <Link
          to="/agent/reports/$id"
          params={{ id: r.id }}
          className="min-w-0 truncate text-[14px] font-semibold hover:underline"
        >
          {r.title}
        </Link>
        {r.status !== "open" && (
          <span className="text-xs text-muted-foreground capitalize">
            {r.status}
          </span>
        )}
      </div>
      <p className="text-[12.5px] text-muted-foreground">
        {[project, r.environment].filter(Boolean).join(" · ")}
        {" · "}seen {r.occurrences} {r.occurrences === 1 ? "time" : "times"}
        {" · "}last seen {ago(r.lastSeenAt, now)}
      </p>
      <p className="flex flex-wrap gap-x-3 text-[12.5px] text-muted-foreground">
        <span>{investigationText(r)}</span>
        <IssueLine issue={r.issue} />
      </p>
    </article>
  )
}
