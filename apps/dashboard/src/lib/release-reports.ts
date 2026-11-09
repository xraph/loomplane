// What a release readiness report says, in words people read. The gates and
// their details come from the server as they are: Loomplane computes them,
// so the page shows them and adds nothing of its own.
import { useEffect } from "react"

import {
  useReleaseReportsGet,
  useReleaseReportsList,
} from "@/client/forge/loomplane/hooks"
import type { ReleaseReport } from "@/client/forge/loomplane/types"
import { useInvalidate } from "@/client/hooks"
import { useRealtime } from "@/lib/realtime-provider"

/** The seven gates, with the same words as GateNames in internal/releaseprep. */
export const gateNames: Record<number, string> = {
  1: "autonomous promotion",
  2: "promotes what staging runs",
  3: "clean plan",
  4: "staging soak",
  5: "safety net",
  6: "nothing open on staging",
  7: "budget",
}

const states: Record<string, string> = {
  planning: "Planning the release",
  reviewing: "The agent is reviewing",
  promoting: "Promoting",
  promoted: "Promoted by the agent",
  waiting_for_person: "Waiting for a person",
  approved: "Approved by a person",
  rejected: "Rejected",
  stale: "The plan went stale",
  expired: "Nobody decided in time",
  superseded: "Replaced by a newer plan",
  blocked: "Blocked by the plan",
  stopped: "Stopped before a decision",
  failed: "Failed",
  cancelled: "Cancelled",
}

/** stateLabel names a report's state for people. */
export function stateLabel(state: string): string {
  return states[state] ?? state
}

export type StateTone =
  "neutral" | "progress" | "success" | "warning" | "danger"

/** stateTone is the tone a state is shown in; it never follows the accent. */
export function stateTone(state: string): StateTone {
  switch (state) {
    case "planning":
    case "reviewing":
    case "promoting":
      return "progress"
    case "promoted":
    case "approved":
      return "success"
    case "waiting_for_person":
    case "stopped":
      return "warning"
    case "blocked":
    case "failed":
    case "rejected":
      return "danger"
    default:
      return "neutral"
  }
}

/** isActive says the report is still going: someone or something is on it. */
export function isActive(state: string): boolean {
  return (
    state === "planning" ||
    state === "reviewing" ||
    state === "promoting" ||
    state === "waiting_for_person"
  )
}

/**
 * mayCancelReport is the server's rule for who stops a preparation: someone
 * who may approve the project's deployments, or the person who asked while
 * they hold agent:use. A report the agent is promoting can't be cancelled:
 * its approval may be landing. The server checks again.
 */
export function mayCancelReport(
  r: Pick<ReleaseReport, "state" | "requestedBy">,
  who: { userId: string; can: (permission: string) => boolean }
): boolean {
  if (!isActive(r.state) || r.state === "promoting") return false
  return (
    who.can("deployment:approve") ||
    (r.requestedBy === who.userId && who.can("agent:use"))
  )
}

/**
 * useReleaseReportEvents refetches the release report list and the open
 * report when the server says a report changed. The list read tags nothing,
 * so the realtime entity binding can't reach it.
 */
export function useReleaseReportEvents(): void {
  const realtime = useRealtime()
  const invalidate = useInvalidate()
  useEffect(() => {
    if (!realtime) return
    const stop = realtime.onReleaseReport(() => {
      invalidate(useReleaseReportsList)
      invalidate(useReleaseReportsGet)
    })
    return () => {
      stop()
    }
  }, [realtime, invalidate])
}
