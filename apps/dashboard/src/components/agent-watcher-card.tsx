// The watcher's state on the AI settings page: whether the agent is looking
// into what goes wrong or stopped because the workspace used its AI budget,
// when it last checked, and which GitHub installations take report issues.
import { useAgentWatcherGet } from "@/client/forge/loomplane/hooks"
import type { AgentwatchStatus } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import {
  SettingsCardHead,
  SettingsSection,
} from "@/components/settings-section"
import { Skeleton } from "@/components/ui/skeleton"
import { ago } from "@/lib/relative-time"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

/** AgentWatcherCard is where the agent's watcher stands. */
export function AgentWatcherCard() {
  const watcher = useQuery(useAgentWatcherGet)
  return (
    <SettingsSection
      title="Watcher"
      description="The agent watches your deployments, canaries and releases, and writes a report when something goes wrong."
    >
      {watcher.status === "error" ? (
        <QueryError
          error={watcher.error}
          fallback="The watcher couldn't be loaded."
          retry={watcher.refetch}
        />
      ) : !watcher.data ? (
        <Skeleton
          data-testid="agent-watcher-loading"
          className="h-40 w-full rounded-[14px]"
        />
      ) : (
        <State status={watcher.data} />
      )}
    </SettingsSection>
  )
}

function State({ status: s }: { status: AgentwatchStatus }) {
  const now = useNow(30_000)
  return (
    <>
      <Panel className="gap-2">
        <SettingsCardHead
          title={s.paused ? "Stopped investigating (budget)" : "Watching"}
          description={
            s.paused
              ? `The workspace used its AI budget for the month${s.pausedAt ? `, ${ago(s.pausedAt, now)}` : ""}. Reports still open and go out as usual.`
              : "Reports open and the agent looks into them."
          }
        />
        <p className="text-[12.5px] text-muted-foreground">
          {s.tickedAt
            ? `Last check ${ago(s.tickedAt, now)}.`
            : "It hasn't checked yet."}{" "}
          <Link
            to="/agent/reports"
            className="font-medium text-foreground underline underline-offset-2"
          >
            See the reports
          </Link>
        </p>
      </Panel>
      <Panel className="gap-2">
        <SettingsCardHead title="GitHub issues" />
        <Issues issues={s.issues} />
      </Panel>
    </>
  )
}

function Issues({ issues }: { issues: AgentwatchStatus["issues"] }) {
  if (!issues.configured)
    return (
      <p className="text-[13px] text-muted-foreground">
        No GitHub App on this server, so reports go to Herald only.
      </p>
    )
  if (issues.installations.length === 0)
    return (
      <p className="text-[13px] text-muted-foreground">
        No GitHub account is connected yet, so reports go to Herald only.
      </p>
    )
  return (
    <ul className="grid gap-2">
      {issues.installations.map((i) => (
        <li
          key={`${i.accountType}/${i.account}`}
          className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 text-[13px]"
        >
          {i.accepted ? (
            <span>
              <span className="font-medium">{i.account}</span>: Accepted
            </span>
          ) : (
            <span>
              {i.account} hasn&apos;t accepted the Issues permission yet, so
              reports for its repositories go to Herald only.
            </span>
          )}
          {!i.accepted && i.manageUrl && (
            <a
              href={i.manageUrl}
              target="_blank"
              rel="noreferrer noopener"
              className="shrink-0 font-medium underline underline-offset-2"
            >
              Review on GitHub
            </a>
          )}
        </li>
      ))}
    </ul>
  )
}
