export type ObservationSummary = {
  desiredReplicas?: number | null
  currentReplicas?: number | null
  readyReplicas?: number | null
  cpuUtilization?: number | null
  cpuUsageMillicores?: number | null
  cpuRequestMillicores?: number | null
  memoryBytes?: number | null
  requests?: number | null
  latencyP95Millis?: number | null
  reason?: string
  sources?: {
    component: string
    source: string
    observedAt?: string | null
    runtimeObservedAt?: string | null
    replicasObservedAt?: string | null
    runtimeLive: boolean
    runtimeIdentity?: string
    reason?: string
  }[]
  partial: boolean
  freshness: string
  oldestObservation?: string | null
  costStatus?: string
  estimatedHourlyMicroUsd?: number | null
  observedCostMicroUsd?: number | null
  coverage: {
    expected: number
    observed: number
    fresh: number
    cpu?: number
    replicas?: number
    memory?: number
    requests?: number
    latency?: number
  }
}

const reasonLabels: Record<string, string> = {
  source_unavailable: "Observation source unavailable",
  source_failed: "Observation source failed",
  source_invalid: "Observation source returned invalid data",
  histogram_incompatible: "Latency histograms cannot be combined",
}

const metric = (value: number | null | undefined, suffix = "") =>
  value == null || !Number.isFinite(value) || value < 0
    ? "Unknown"
    : `${value.toLocaleString()}${suffix}`

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
        : summary?.freshness === "partial"
          ? "Partially fresh observations"
          : summary?.freshness === "historical"
            ? "Historical observations"
            : "Observation unavailable"
  const money = (amount: number | null | undefined) => {
    if (amount == null || !Number.isSafeInteger(amount) || amount < 0)
      return null
    return amount > 0 && amount < 10000
      ? "<$0.01"
      : `$${(amount / 1000000).toFixed(2)}`
  }
  const estimated = money(summary?.estimatedHourlyMicroUsd)
  const observed = money(summary?.observedCostMicroUsd)
  const cost =
    summary?.costStatus === "estimated" && estimated != null
      ? `Estimated ${estimated}/hour`
      : summary?.costStatus === "observed" && observed != null
        ? `Observed ${observed} in window`
        : "Unknown"
  const coverage = (count: number | undefined) =>
    count == null || !summary || summary.coverage.expected === 0
      ? "Coverage unknown"
      : `${count} of ${summary.coverage.expected} components`
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
          {summary && (
            <dd className="text-xs text-muted-foreground">
              {coverage(summary.coverage.replicas)}
            </dd>
          )}
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">CPU utilization</dt>
          <dd aria-label="CPU utilization" className="font-medium tabular-nums">
            {cpu}
          </dd>
          {summary && (
            <dd className="text-xs text-muted-foreground">
              {coverage(summary.coverage.cpu)}
            </dd>
          )}
        </div>
        {[
          {
            label: "CPU used / requested",
            value: `${metric(summary?.cpuUsageMillicores)} / ${metric(summary?.cpuRequestMillicores)} mCPU`,
            count: summary?.coverage.cpu,
          },
          {
            label: "Memory",
            value:
              summary?.memoryBytes == null
                ? "Unknown"
                : metric(
                    Math.round((summary.memoryBytes / 1048576) * 100) / 100,
                    " MiB"
                  ),
            count: summary?.coverage.memory,
          },
          {
            label: "Requests in window",
            value: metric(summary?.requests),
            count: summary?.coverage.requests,
          },
          {
            label: "p95 latency bucket bound",
            value: metric(summary?.latencyP95Millis, " ms"),
            count: summary?.coverage.latency,
          },
        ].map(({ label, value, count }) => (
          <div key={label}>
            <dt className="text-xs text-muted-foreground">{label}</dt>
            <dd aria-label={label} className="font-medium tabular-nums">
              {value}
            </dd>
            {summary && (
              <dd className="text-xs text-muted-foreground">
                {coverage(count)}
              </dd>
            )}
          </div>
        ))}
        <div>
          <dt className="text-xs text-muted-foreground">Cost</dt>
          <dd aria-label="Cost" className="font-medium">
            {cost}
          </dd>
        </div>
      </dl>
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span>{freshness}</span>
        {summary?.reason && reasonLabels[summary.reason] && (
          <span>{reasonLabels[summary.reason]}</span>
        )}
        {summary?.partial && (
          <span>Values cover reporting components only.</span>
        )}
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
      {!!summary?.sources?.length && (
        <details className="min-w-0 text-xs text-muted-foreground">
          <summary className="cursor-pointer rounded-sm py-1 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring">
            Observation sources
          </summary>
          <ul className="mt-1 grid gap-2">
            {summary.sources.map((source) => (
              <li
                key={source.component}
                className="grid min-w-0 gap-1 break-words"
              >
                <span className="font-medium">
                  {source.component} · {source.source || "Source unavailable"}
                </span>
                <div className="flex flex-wrap gap-x-3 gap-y-1">
                  {source.runtimeLive && (
                    <span>Current runtime observation</span>
                  )}
                  {source.runtimeIdentity === "pod_name_window" && (
                    <span>Pod names within sample window</span>
                  )}
                  {source.runtimeIdentity === "empty_controller" && (
                    <span>Empty controller</span>
                  )}
                  {source.reason && reasonLabels[source.reason] && (
                    <span>{reasonLabels[source.reason]}</span>
                  )}
                  {[
                    ["Traffic sample", source.observedAt],
                    ["Runtime sample", source.runtimeObservedAt],
                    ["Replica sample", source.replicasObservedAt],
                  ].map(
                    ([label, stamp]) =>
                      stamp && (
                        <span key={label}>
                          {label}:{" "}
                          <time dateTime={stamp}>
                            {new Date(stamp).toLocaleString()}
                          </time>
                        </span>
                      )
                  )}
                </div>
              </li>
            ))}
          </ul>
        </details>
      )}
    </section>
  )
}
