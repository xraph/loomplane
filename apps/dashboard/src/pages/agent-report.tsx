// One report: what happened, what Loomplane already did, what the agent
// makes of it, the records it is about and where it went on GitHub. Facts
// that came from outside Loomplane (an error message, a verdict reason) are
// shown as text in a monospace block, never as markup.
import { useState, type FormEvent, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAgentReportsClose,
  useAgentReportsGet,
  useAgentReportsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Fact, Report } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { Textarea } from "@/components/ui/textarea"
import { useFeatures } from "@/lib/features"
import { useProjectCan } from "@/lib/project-permissions"
import { ago } from "@/lib/relative-time"
import { Link } from "@/lib/router"
import { useNow, usePeople, isAgentActor } from "@/lib/use-runs"
import { IssueLine, SeverityDot, investigationText } from "./agent-reports"

/** What Loomplane did on its own, by the signal that raised the report. */
function alreadyDid(signal: string): string {
  switch (signal) {
    case "canary_rolled_back":
      return "The canary rolled traffic back to the previous revision."
    case "watch_rolled_back":
      return "Loomplane rolled the release back automatically."
    default:
      return "Nothing yet: a person needs to decide."
  }
}

/** AgentReport is the page of one report. */
export function AgentReport({ id }: { id: string }) {
  const features = useFeatures()
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Agent reports", link: <Link to="/agent/reports" /> },
          { label: "Report" },
        ]}
      />
      <Page className="max-w-[880px]">{features.ai && <Loaded id={id} />}</Page>
    </>
  )
}

function Loaded({ id }: { id: string }) {
  const report = useQuery(useAgentReportsGet, { path: { id } })
  if (report.status === "error")
    return (
      <QueryError
        error={report.error}
        fallback="The report couldn't be loaded."
        retry={report.refetch}
      />
    )
  if (!report.data) return <Skeleton className="h-64 w-full rounded-[14px]" />
  return <Detail report={report.data} />
}

function Detail({ report: r }: { report: Report }) {
  const can = useProjectCan(r.projectId)
  const projects = useQuery(useProjectsList)
  const project = projects.data?.projects.find((p) => p.id === r.projectId)
  const now = useNow(30_000)
  const who = usePeople()
  const mayClose = r.status === "open" && can("deployment:create")
  return (
    <>
      <PageHeading
        title={r.title}
        description={
          [project?.name, r.environment].filter(Boolean).join(" · ") ||
          undefined
        }
      />
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="flex items-center gap-2 text-[13px]">
          <SeverityDot severity={r.severity} />
          <span className="capitalize">{r.severity}</span>
          <span className="text-muted-foreground capitalize">
            {" · "}
            {r.status}
          </span>
        </p>
        {mayClose && <CloseReport report={r} />}
      </div>
      {r.status === "closed" && (
        <p className="text-[13px] text-muted-foreground">
          Closed
          {r.closedBy
            ? ` by ${isAgentActor(r.closedBy) ? "the agent" : who(r.closedBy).name}`
            : ""}
          {r.resolution ? `: ${r.resolution}` : "."}
        </p>
      )}
      <Section title="What happened">
        {r.facts.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">
            No details were recorded.
          </p>
        ) : (
          <dl className="grid gap-2.5">
            {r.facts.map((f, i) => (
              <FactRow key={`${f.label}-${i}`} fact={f} />
            ))}
          </dl>
        )}
      </Section>
      <Section title="What Loomplane already did">
        <p className="text-[13px]">{alreadyDid(r.signal)}</p>
      </Section>
      <Section title="What the agent thinks">
        <Thinks report={r} />
      </Section>
      <Section title="Records">
        <Records report={r} />
      </Section>
      <Section title="Where it went">
        <p className="text-[13px] text-muted-foreground">
          <IssueLine issue={r.issue} />
          {r.repository && r.issue.url ? ` in ${r.repository}` : ""}
        </p>
      </Section>
      <p className="text-[12.5px] text-muted-foreground">
        Seen {r.occurrences} {r.occurrences === 1 ? "time" : "times"}. First
        seen {ago(r.firstSeenAt, now)}, last seen {ago(r.lastSeenAt, now)}.
      </p>
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel aria-label={title} className="gap-2.5">
      <h2 className="text-[13px] font-semibold">{title}</h2>
      {children}
    </Panel>
  )
}

// An internal path a fact names, such as /deployments/dep_1. Anything else
// a fact calls a path is shown as text.
const deploymentPath = /^\/deployments\/([\w.-]+)$/

function FactRow({ fact }: { fact: Fact }) {
  const target = fact.path ? deploymentPath.exec(fact.path)?.[1] : undefined
  return (
    <div className="grid gap-1">
      <dt className="text-[12.5px] text-muted-foreground">{fact.label}</dt>
      <dd className="min-w-0 text-[13px]">
        {fact.untrusted ? (
          <pre className="max-h-64 overflow-auto rounded-lg bg-subtle px-3 py-2 font-mono text-[12px] leading-relaxed whitespace-pre-wrap">
            {fact.value}
          </pre>
        ) : target ? (
          <Link
            to="/deployments/$id"
            params={{ id: target }}
            className="underline underline-offset-2"
          >
            {fact.value}
          </Link>
        ) : (
          <span className="break-words">{fact.value}</span>
        )}
      </dd>
    </div>
  )
}

function Thinks({ report: r }: { report: Report }) {
  const f = r.findings
  if (!f)
    return (
      <p className="text-[13px] text-muted-foreground">
        {r.investigation === "none" || r.investigation === "queued"
          ? "The agent hasn't looked at this yet."
          : investigationText(r)}
      </p>
    )
  return (
    <div className="grid gap-2 text-[13px]">
      <p>{f.summary}</p>
      {f.likelyCause && (
        <p>
          <span className="font-medium">Likely cause:</span> {f.likelyCause}
        </p>
      )}
      {f.suggestions.length > 0 && (
        <div>
          <p className="font-medium">What to try</p>
          <ul className="ml-4 list-disc">
            {f.suggestions.map((s, i) => (
              <li key={i}>{s}</li>
            ))}
          </ul>
        </div>
      )}
      {f.confidence && (
        <p className="text-muted-foreground">Confidence: {f.confidence}</p>
      )}
    </div>
  )
}

function Records({ report: r }: { report: Report }) {
  const e = r.evidence
  const rows: { label: string; id: string; deployment?: boolean }[] = [
    { label: "Deployment", id: e.deploymentId ?? "", deployment: true },
    { label: "Plan", id: e.planId ?? "", deployment: true },
    { label: "Binding", id: e.bindingId ?? "" },
    { label: "Watch", id: e.watchId ?? "" },
    { label: "Analysis", id: e.analysisId ?? "" },
  ].filter((row) => row.id)
  if (rows.length === 0)
    return (
      <p className="text-[13px] text-muted-foreground">
        No records are tied to this report.
      </p>
    )
  return (
    <ul className="grid gap-1 text-[13px]">
      {rows.map((row) => (
        <li key={row.label} className="flex gap-2">
          <span className="w-24 text-muted-foreground">{row.label}</span>
          {row.deployment ? (
            <Link
              to="/deployments/$id"
              params={{ id: row.id }}
              className="font-mono text-[12px] underline underline-offset-2"
            >
              {row.id}
            </Link>
          ) : (
            <span className="font-mono text-[12px]">{row.id}</span>
          )}
        </li>
      ))}
    </ul>
  )
}

function CloseReport({ report: r }: { report: Report }) {
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState("")
  const [error, setError] = useState("")
  const close = useMutation(useAgentReportsClose)
  const invalidate = useInvalidate()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      await close.mutateAsync({
        path: { id: r.id },
        body: reason.trim() ? { reason: reason.trim() } : {},
      })
      invalidate(useAgentReportsGet)
      invalidate(useAgentReportsList)
      setOpen(false)
      setReason("")
      toast.success("Report closed")
    } catch (err) {
      setError(messageOf(err, "The report couldn't be closed."))
    }
  }

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Close
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={(e) => void submit(e)} className="grid gap-4">
            <DialogHeader>
              <DialogTitle>Close this report</DialogTitle>
              <DialogDescription>
                {r.issue.url
                  ? "Loomplane comments on the GitHub issue and closes it."
                  : "The report moves out of the open list."}
              </DialogDescription>
            </DialogHeader>
            <label className="grid gap-1.5 text-[12.5px] font-medium text-muted-foreground">
              Reason (optional)
              <Textarea
                aria-label="Reason"
                value={reason}
                maxLength={500}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setOpen(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={close.isPending}>
                {close.isPending && <Spinner data-icon="inline-start" />}
                Close report
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
