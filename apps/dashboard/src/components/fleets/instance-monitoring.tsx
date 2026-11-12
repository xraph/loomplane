import { useCallback, useState } from "react"
import { statusOf } from "@/client/errors"
import {
  useFleetEventsList,
  useFleetMetricsGet,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { FleetReadState } from "@/components/fleets/read-state"
import { useFleetRefresh } from "@/components/fleets/use-fleet-refresh"
import { Button } from "@/components/ui/button"
import { EventJournal } from "./event-journal"
import { Observations } from "./observations"

// Mount after the instance scope is allowed; key by project and instance identity.
export function InstanceMonitoring({
  projectID,
  instanceID,
}: {
  projectID: string
  instanceID: string
}) {
  const [after, setAfter] = useState("")
  const events = useQuery(useFleetEventsList, {
    path: { project_id: projectID, id: instanceID },
    query: { after, limit: 50 },
  })
  const metrics = useQuery(useFleetMetricsGet, {
    path: { project_id: projectID, id: instanceID },
  })
  const { isFetching: fetchingEvents, refetch: refetchEvents } = events
  const { isFetching: fetchingMetrics, refetch: refetchMetrics } = metrics
  const eventAllowed = ![401, 403, 404].includes(statusOf(events.error))
  const metricAllowed = ![401, 403, 404].includes(statusOf(metrics.error))
  const refreshMetrics = useCallback(async () => {
    if (!metricAllowed || fetchingMetrics) return
    await refetchMetrics()
  }, [metricAllowed, fetchingMetrics, refetchMetrics])
  const refreshEvents = useCallback(async () => {
    if (!eventAllowed || fetchingEvents) return
    await refetchEvents()
  }, [eventAllowed, fetchingEvents, refetchEvents])
  useFleetRefresh(metricAllowed && !fetchingMetrics, refreshMetrics)
  useFleetRefresh(eventAllowed && !fetchingEvents && !after, refreshEvents)
  return (
    <div className="grid min-w-0 gap-4">
      <section aria-label="Instance monitoring" className="grid min-w-0 gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-medium">
            Observations{" "}
            <span className="font-normal text-muted-foreground">
              · Last 15 minutes
            </span>
          </h2>
          {metricAllowed && (
            <Button
              size="sm"
              variant="outline"
              disabled={fetchingMetrics}
              onClick={() => void refreshMetrics().catch(() => undefined)}
            >
              Refresh observations
            </Button>
          )}
        </div>
        {metrics.status === "error" ? (
          <FleetReadState
            projectID={projectID}
            error={metrics.error}
            retry={refreshMetrics}
          />
        ) : !metrics.data ? (
          <p
            role="status"
            aria-busy="true"
            className="text-sm text-muted-foreground"
          >
            Loading observations
          </p>
        ) : (
          <Observations summary={metrics.data} />
        )}
      </section>
      <div className="grid min-w-0 gap-2">
        {after && eventAllowed && (
          <Button
            className="justify-self-start"
            size="sm"
            variant="outline"
            disabled={fetchingEvents}
            onClick={() => setAfter("")}
          >
            First event page
          </Button>
        )}
        <EventJournal
          projectID={projectID}
          page={events.status === "error" ? undefined : events.data}
          error={events.status === "error" ? (events.error ?? {}) : undefined}
          loading={fetchingEvents || !events.data}
          loadingMore={fetchingEvents}
          continuation={!!after}
          retry={refreshEvents}
          loadMore={async (cursor) => {
            if (fetchingEvents || !eventAllowed) return
            if (cursor === after) await refetchEvents()
            else setAfter(cursor)
          }}
        />
      </div>
    </div>
  )
}
