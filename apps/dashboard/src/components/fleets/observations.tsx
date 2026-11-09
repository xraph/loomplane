export type ObservationSummary = {
  desiredReplicas?: number | null
  currentReplicas?: number | null
  readyReplicas?: number | null
  cpuUtilization?: number | null
  partial: boolean
  freshness: string
  oldestObservation?: string | null
  coverage: { expected: number; observed: number; fresh: number }
}

export function Observations({ summary }: { summary?: ObservationSummary }) {
  const replicas = [
    summary?.desiredReplicas,
    summary?.currentReplicas,
    summary?.readyReplicas,
  ]
    .map((value) => value ?? "Unknown")
    .join(" / ")
  const cpu =
    summary?.cpuUtilization == null
      ? "Unknown"
      : `${Math.round(summary.cpuUtilization * 100)}%`
  const freshness =
    summary?.freshness === "fresh"
      ? "Fresh observation"
      : summary?.freshness === "stale"
        ? "Stale observation"
        : "Observation unavailable"
  return (
    <section
      aria-label="Runtime observations"
      className="grid gap-2 rounded-lg border p-3"
    >
      <dl className="flex flex-wrap gap-x-8 gap-y-3 text-sm">
        <div>
          <dt className="text-xs text-muted-foreground">
            Desired / current / ready replicas
          </dt>
          <dd
            aria-label="Desired / current / ready replicas"
            className="font-medium tabular-nums"
          >
            {replicas}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">CPU utilization</dt>
          <dd aria-label="CPU utilization" className="font-medium tabular-nums">
            {cpu}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">Cost</dt>
          <dd className="font-medium">Unknown</dd>
        </div>
      </dl>
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span>{freshness}</span>
        {summary?.partial && (
          <span>
            Partial: {summary.coverage.observed} of {summary.coverage.expected}{" "}
            components observed
          </span>
        )}
        {summary?.oldestObservation && (
          <span>
            Oldest sample:{" "}
            <time dateTime={summary.oldestObservation}>
              {new Date(summary.oldestObservation).toLocaleString()}
            </time>
          </span>
        )}
      </div>
    </section>
  )
}
