import { HistoryIcon } from "lucide-react"
import { statusOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { Link } from "@/lib/router"

export type JournalEvent = {
  id: string
  sequence: number
  kind: string
  generation: number
  occurredAt: string
  operationId?: string
  lifecycle?: { state: string; reason?: string } | null
  scaling?: {
    service: string
    previousReplicas: number
    desiredReplicas: number
    reason: string
    writer: string
    policyRevision: number
    cloudId?: string
  } | null
  release?: {
    campaignId: string
    wave: number
    state: string
    reason?: string
    targetGeneration?: number
    targetOperationId?: string
  } | null
}

export type JournalPage = {
  items: JournalEvent[]
  nextCursor?: string
  replayTruncated: boolean
}

const label = (code: string) =>
  code.charAt(0).toUpperCase() + code.slice(1).replaceAll("_", " ")

export function EventJournal({
  projectID,
  page,
  loading,
  error,
  retry,
  loadMore,
  loadingMore,
  continuation,
}: {
  projectID: string
  page?: JournalPage
  loading?: boolean
  error?: unknown
  retry: () => Promise<unknown>
  loadMore?: (cursor: string) => Promise<unknown>
  loadingMore?: boolean
  continuation?: boolean
}) {
  if (error) {
    const status = statusOf(error)
    const denied = status === 401 || status === 403
    const missing = status === 404
    return (
      <ZeroState
        variant="illustrated"
        preview={false}
        role={denied || missing ? "status" : "alert"}
      >
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <HistoryIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>
            {denied
              ? "Event access denied"
              : missing
                ? "Events unavailable"
                : "Could not load events"}
          </ZeroStateTitle>
          <ZeroStateDescription>
            {denied
              ? "Your current permissions do not allow this event history."
              : missing
                ? "This instance may have been removed or belongs to another project."
                : "Try again to read the current event history."}
          </ZeroStateDescription>
        </ZeroStateHeader>
        {!denied && !missing && (
          <ZeroStateContent>
            <Button
              size="sm"
              variant="outline"
              onClick={() => void retry().catch(() => undefined)}
            >
              Retry events
            </Button>
          </ZeroStateContent>
        )}
      </ZeroState>
    )
  }
  if (!page) {
    return (
      <p
        role="status"
        aria-busy={loading || undefined}
        className="py-3 text-sm text-muted-foreground"
      >
        {loading ? "Loading events" : "Event history has not been loaded."}
      </p>
    )
  }
  if (page.items.length === 0) {
    return (
      <ZeroState variant="illustrated">
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <HistoryIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>
            {page.replayTruncated
              ? "No retained events"
              : continuation
                ? "No later events"
                : "No events yet"}
          </ZeroStateTitle>
          <ZeroStateDescription>
            {page.replayTruncated
              ? "Earlier events are no longer available in this journal. Refresh to check for new events."
              : continuation
                ? "No later decisions or operation updates have been recorded. Check again for new events."
                : "Instance decisions and operation progress appear here when recorded."}
          </ZeroStateDescription>
        </ZeroStateHeader>
        <ZeroStateContent>
          <Button
            size="sm"
            variant="outline"
            disabled={loading || loadingMore}
            onClick={() =>
              void (
                page.nextCursor && loadMore
                  ? loadMore(page.nextCursor)
                  : retry()
              ).catch(() => undefined)
            }
          >
            {continuation || page.nextCursor
              ? "Check for later events"
              : "Refresh events"}
          </Button>
        </ZeroStateContent>
      </ZeroState>
    )
  }
  return (
    <section aria-label="Event history" className="grid min-w-0 gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-medium">Events</h2>
        <Button
          size="sm"
          variant="outline"
          disabled={loading}
          onClick={() => void retry().catch(() => undefined)}
        >
          {loading ? "Refreshing events" : "Refresh events"}
        </Button>
      </div>
      {page.replayTruncated && (
        <p role="status" className="text-xs text-muted-foreground">
          Earlier events are no longer available in this journal.
        </p>
      )}
      <ol className="divide-y rounded-lg border px-3">
        {page.items.map((event) => {
          const scaling = event.kind === "scaling" ? event.scaling : undefined
          const release = event.kind === "release" ? event.release : undefined
          const lifecycle =
            event.kind === "lifecycle" ? event.lifecycle : undefined
          const state = lifecycle?.state ?? release?.state
          const reason = lifecycle?.reason ?? scaling?.reason ?? release?.reason
          return (
            <li key={event.id} className="grid min-w-0 gap-1 py-2 text-sm">
              <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                <span className="min-w-0 font-medium break-words">
                  {scaling
                    ? `${scaling.service}: ${scaling.previousReplicas} to ${scaling.desiredReplicas} replicas requested`
                    : state
                      ? label(state)
                      : "Event details unavailable"}
                </span>
                <time
                  dateTime={event.occurredAt}
                  className="text-xs text-muted-foreground"
                >
                  {new Date(event.occurredAt).toLocaleString()}
                </time>
              </div>
              {reason && (
                <p className="text-xs text-muted-foreground">{label(reason)}</p>
              )}
              <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground [&>span]:break-all">
                <span>Sequence {event.sequence}</span>
                <span>Generation {event.generation}</span>
                {event.operationId && (
                  <span>Operation {event.operationId}</span>
                )}
                {scaling && (
                  <span>
                    Policy {scaling.policyRevision} ·{" "}
                    {scaling.writer === "hpa"
                      ? "Automatic scaling"
                      : "Manual scaling"}
                  </span>
                )}
                {release && (
                  <span>
                    Campaign {release.campaignId} · Wave {release.wave}
                  </span>
                )}
                {!!release?.targetGeneration && (
                  <span>Target generation {release.targetGeneration}</span>
                )}
                {release?.targetOperationId && (
                  <span>Target operation {release.targetOperationId}</span>
                )}
                {scaling?.cloudId && (
                  <Link
                    to="/clouds/$id"
                    params={{ id: scaling.cloudId }}
                    search={{ project: projectID, tab: "capacity" }}
                    className="text-primary underline underline-offset-4"
                  >
                    Cloud capacity
                  </Link>
                )}
              </div>
            </li>
          )
        })}
      </ol>
      {page.nextCursor && loadMore && (
        <Button
          className="justify-self-start"
          size="sm"
          variant="outline"
          disabled={loadingMore}
          onClick={() => void loadMore(page.nextCursor!).catch(() => undefined)}
        >
          {loadingMore ? "Loading next events" : "Next events"}
        </Button>
      )}
    </section>
  )
}
