// The post-deploy watch on a deployment's page: how long Loomplane keeps
// judging the release, each service's verdict, the services it can't
// judge and why, and the rollback Loomplane automation started when a
// release breached its health checks.
import { TriangleAlertIcon } from "lucide-react"

import { statusOf } from "@/client/errors"
import { useReleaseWatchesForDeployment } from "@/client/forge/loomplane/hooks"
import type { Watch } from "@/client/forge/loomplane/types"
import { useQuery, useRefetchWhile } from "@/client/hooks"
import { DotProgress } from "@/components/dots"
import { Panel, PanelHead } from "@/components/panel"
import { toneClass } from "@/components/runs"
import { Badge } from "@/components/ui/badge"
import { buttonVariants } from "@/components/ui/button"
import { dateTime } from "@/lib/format"
import { Link } from "@/lib/router"
import { AUTOMATION_NAME, useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

type Tone = keyof typeof toneClass

// Status colours keep their own tokens, never the accent.
const statusTone: Record<string, Tone> = {
  watching: "flight",
  passed: "live",
  inconclusive: "waiting",
  superseded: "quiet",
  rolling_back: "flight",
  rolled_back: "waiting",
  rollback_blocked: "failed",
  rollback_failed: "failed",
}

export const watchStatusLabel: Record<string, string> = {
  watching: "Watching",
  passed: "Passed",
  inconclusive: "Inconclusive",
  superseded: "Superseded",
  rolling_back: "Rolling back",
  rolled_back: "Rolled back",
  rollback_blocked: "Rollback blocked",
  rollback_failed: "Rollback failed",
}

const verdictTone: Record<string, Tone> = {
  pass: "live",
  fail: "failed",
  inconclusive: "waiting",
}

/** open is a watch that still judges the release or follows its rollback. */
function isOpen(w: Watch): boolean {
  return w.status === "watching" || w.status === "rolling_back"
}

// hhmm is a time of day as loomctl prints it: 24-hour, local.
export function hhmm(ts: number): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function verdictWord(verdict: string, open: boolean): string {
  switch (verdict) {
    case "pass":
      return open ? "Passing" : "Passed"
    case "fail":
      return open ? "Failing" : "Failed"
    case "inconclusive":
      return "Couldn't be judged"
  }
  return "Not judged yet"
}

function Tinted({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  const t = toneClass[tone]
  return (
    <Badge variant="secondary" className={cn(t.tint, t.ink)}>
      {children}
    </Badge>
  )
}

/** What the watch came to, in a sentence, before the reason it gives. */
function summary(w: Watch): string {
  switch (w.status) {
    case "passed":
      return "Every watched service stayed healthy through the watch."
    case "inconclusive":
      return "Loomplane couldn't confirm this release was healthy, so nothing was rolled back."
    case "superseded":
      return "A newer release replaced this one before the watch finished, so it wasn't judged to the end."
    case "rolling_back":
      return `This release breached its health checks. ${AUTOMATION_NAME} is rolling it back.`
    case "rolled_back":
      return `This release breached its health checks, and ${AUTOMATION_NAME} rolled it back.`
    case "rollback_blocked":
      return "This release breached its health checks, but Loomplane couldn't roll it back. Roll back yourself, or deploy a fix."
    case "rollback_failed":
      return "This release breached its health checks, and the rollback failed. Check the rollback, then retry it or deploy a fix."
  }
  return ""
}

function Window({ w }: { w: Watch }) {
  const now = useNow()
  const start = Date.parse(w.startsAt)
  const end = Date.parse(w.endsAt)
  const span = end - start
  return (
    <div className="grid gap-1.5">
      <p className="text-[13px] font-medium" title={dateTime(end)}>
        Watching until {hhmm(end)}
      </p>
      {span > 0 && (
        <DotProgress
          size="sm"
          value={(now - start) / span}
          label="How far through the watch"
        />
      )}
    </div>
  )
}

function Services({ w }: { w: Watch }) {
  const open = isOpen(w)
  if (w.services.length === 0 && w.unwatched.length === 0) return null
  return (
    <ul
      aria-label="Watched services"
      className="grid divide-y rounded-lg ring-1 ring-border"
    >
      {w.services.map((s) => {
        const t = toneClass[verdictTone[s.verdict] ?? "quiet"]
        return (
          <li key={s.bindingId || s.name} className="grid gap-0.5 px-3 py-2">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-[13px] font-medium">{s.name}</span>
              <span className="text-[12.5px] text-muted-foreground">
                {s.provider}
              </span>
              <span
                className={cn(
                  "ml-auto flex items-center gap-1.5 text-[12.5px]",
                  t.ink
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn("size-1.5 rounded-full", t.dot)}
                />
                {verdictWord(s.verdict, open)}
              </span>
            </div>
            {s.reason && (
              <p className="text-[12.5px] text-muted-foreground">{s.reason}</p>
            )}
          </li>
        )
      })}
      {w.unwatched.map((u) => (
        <li key={`unwatched:${u.name}`} className="grid gap-0.5 px-3 py-2">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-[13px] font-medium">{u.name}</span>
            <span className="text-[12.5px] text-muted-foreground">
              {u.provider}
            </span>
          </div>
          <p className="text-[12.5px] text-muted-foreground">
            Not watched: {u.reason}
          </p>
        </li>
      ))}
    </ul>
  )
}

function WatchBody({ w }: { w: Watch }) {
  const breached = [
    "rolling_back",
    "rolled_back",
    "rollback_blocked",
    "rollback_failed",
  ].includes(w.status)
  const line = summary(w)
  return (
    <div className="mt-3 grid gap-3">
      {w.status === "watching" && <Window w={w} />}
      {line && (
        <div
          role={
            w.status === "rollback_blocked" || w.status === "rollback_failed"
              ? "alert"
              : undefined
          }
          className={cn(
            "grid gap-1 text-[13px]",
            breached &&
              "rounded-lg px-3 py-2.5 ring-1 " +
                (w.status === "rollback_blocked" ||
                w.status === "rollback_failed"
                  ? "bg-failed-tint text-destructive ring-failed/30"
                  : "bg-subtle ring-border")
          )}
        >
          <p className="flex gap-2">
            {breached && (
              <TriangleAlertIcon
                aria-hidden="true"
                className="mt-0.5 size-4 shrink-0"
              />
            )}
            <span>{line}</span>
          </p>
          {w.reason && (
            <p
              className={cn(
                "text-[12.5px]",
                breached ? "pl-6" : "text-muted-foreground"
              )}
            >
              {w.reason}
            </p>
          )}
          {w.rollbackPlanId && (
            <Link
              to="/deployments/$id"
              params={{ id: w.rollbackPlanId }}
              className={cn(
                buttonVariants({ variant: "link", size: "sm" }),
                "h-auto w-fit px-0 pl-6"
              )}
            >
              Open the rollback
            </Link>
          )}
        </div>
      )}
      <Services w={w} />
      {w.thresholds && (
        <p className="text-[12.5px] text-muted-foreground">
          A service fails above {pct(w.thresholds.errorRate)} errors; a verdict
          needs at least {w.thresholds.minRequests} requests.
        </p>
      )}
    </div>
  )
}

function pct(n: number): string {
  return `${Math.round(n * 1000) / 10}%`
}

/**
 * ReleaseWatchPanel shows a deployment's post-deploy watch. A deployment
 * with no watch shows nothing.
 */
export function ReleaseWatchPanel({ deploymentId }: { deploymentId: string }) {
  const watch = useQuery(useReleaseWatchesForDeployment, {
    path: { id: deploymentId },
  })
  const w = watch.data
  // The stream updates the watch as it's judged; this covers a stream
  // that's down.
  useRefetchWhile(w !== undefined && isOpen(w), watch.refetch, 15000)
  if (watch.status === "error" && statusOf(watch.error) === 404) return null
  if (!w && watch.status !== "error") return null
  const unverified = w ? w.unverified || w.unwatched.length > 0 : false
  return (
    <Panel aria-label="Release watch">
      <PanelHead
        title="Release watch"
        aside={
          w && (
            <span className="flex items-center gap-1.5">
              {unverified && <Tinted tone="waiting">Unverified</Tinted>}
              <Tinted tone={statusTone[w.status] ?? "quiet"}>
                {watchStatusLabel[w.status] ?? w.status}
              </Tinted>
            </span>
          )
        }
      />
      {w ? (
        <WatchBody w={w} />
      ) : (
        <p className="mt-2 text-[13px] text-muted-foreground">
          The release watch couldn&apos;t be read.
        </p>
      )}
    </Panel>
  )
}
