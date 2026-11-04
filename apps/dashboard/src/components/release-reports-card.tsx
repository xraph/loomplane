// A project's latest release preparations, with how each stands. It shows
// only while the server runs the agent.
import { useReleaseReportsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { Panel, PanelHead } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { Skeleton } from "@/components/ui/skeleton"
import { useAgent, useFeatures } from "@/lib/features"
import {
  stateLabel,
  stateTone,
  useReleaseReportEvents,
  type StateTone,
} from "@/lib/release-reports"
import { ago } from "@/lib/relative-time"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

const toneOf: Record<StateTone, Tone> = {
  neutral: "quiet",
  progress: "flight",
  success: "live",
  warning: "waiting",
  danger: "failed",
}

/** ReleaseReportsCard lists a project's five newest release reports. */
export function ReleaseReportsCard({ projectId }: { projectId: string }) {
  const features = useFeatures()
  if (!features.ai) return null
  return <Reports projectId={projectId} />
}

function Reports({ projectId }: { projectId: string }) {
  useReleaseReportEvents()
  const agent = useAgent()
  const now = useNow(30_000)
  const list = useQuery(useReleaseReportsList, {
    path: { id: projectId },
    query: { limit: 5 },
  })
  const reports = (list.data?.reports ?? []).flatMap((r) => (r ? [r] : []))
  // With the agent off and nothing prepared before, there's nothing to show.
  if (list.data && reports.length === 0 && !agent.enabled) return null
  return (
    <Panel aria-label="Release reports" className="gap-3">
      <PanelHead title="Release reports" aside="Latest five" />
      {list.status === "error" ? (
        <QueryError
          error={list.error}
          fallback="The release reports couldn't be loaded."
          retry={list.refetch}
        />
      ) : !list.data ? (
        <Skeleton className="h-16 w-full rounded-lg" />
      ) : reports.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          No release has been prepared yet. Prepare one to see whether the agent
          may promote it, and why a person has to.
        </p>
      ) : (
        <ul className="grid divide-y divide-border">
          {reports.map((r) => (
            <li key={r.id}>
              <Link
                to="/release-reports/$id"
                params={{ id: r.id }}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-[13px] hover:text-primary"
              >
                <span className="font-medium">
                  {r.fromEnvironment} to {r.toEnvironment}
                </span>
                <StatusPill tone={toneOf[stateTone(r.state)]}>
                  {stateLabel(r.state)}
                </StatusPill>
                <span className="ml-auto text-xs text-muted-foreground">
                  {ago(r.createdAt, now)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}
