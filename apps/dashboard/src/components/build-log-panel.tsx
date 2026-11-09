import { InfoIcon } from "lucide-react"
import { useMemo, useState } from "react"
import { statusOf } from "@/client/errors"
import { notices } from "@/lib/build-notices"
import { CloudCallout, Segments } from "@/components/cloud-targets/cloud-page"
import { CloudEmpty } from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import {
  LogViewer,
  type LogLine,
  type Tone,
  toneClass,
} from "@/components/runs"
import { useBuildLogs } from "@/lib/build-logs"
import { useLiveState } from "@/lib/realtime-state"
import { cn } from "@/lib/utils"

/**
 * A LogSource is one choice in the log viewer's source switcher: every
 * line, the plan's own record, or one service's step. steps names the
 * thread steps whose lines it shows; null shows them all.
 */
export interface LogSource {
  id: string
  label: string
  steps: string[] | null
  /** How the step is doing, drawn as a dot beside its name. */
  tone?: Tone
}

/**
 * BuildLogPanel is a deployment's logs: what Loomplane recorded, joined
 * with the providers' retained build output, filtered to one source.
 */
export function BuildLogPanel({
  deploymentId,
  activity,
  live,
  name,
  sources,
  source,
  onSource,
}: {
  deploymentId?: string
  activity: LogLine[]
  live: boolean
  name: string
  sources: LogSource[]
  source: string
  onSource: (id: string) => void
}) {
  const output = useBuildLogs(deploymentId)
  const connection = useLiveState()
  const [attempt, setAttempt] = useState("all")
  const attempts = [...new Set(output.logs.map((line) => line.attempt))]
  const picked = sources.find((s) => s.id === source) ?? sources[0]
  const all = useMemo(
    () =>
      [
        ...(attempt === "all" ? activity : []),
        ...output.logs
          .filter((line) => attempt === "all" || line.attempt === attempt)
          .map((line) => ({
            id: line.id,
            ts: Date.parse(line.time),
            level: (line.stream === "stderr" || line.level === "ERROR"
              ? "error"
              : line.level === "WARNING"
                ? "warn"
                : "info") as LogLine["level"],
            source: line.source || "build",
            text: `[${line.attempt}] ${line.message}${line.truncated ? " [truncated]" : ""}`,
            step: line.actionId,
          })),
      ]
        .sort((a, b) => a.ts - b.ts)
        .slice(-2000),
    [activity, output.logs, attempt]
  )
  const lines = useMemo(() => {
    const steps = picked?.steps
    return steps ? all.filter((l) => !!l.step && steps.includes(l.step)) : all
  }, [all, picked])

  if (connection.status === "denied" || statusOf(output.error) === 403)
    return (
      <CloudEmpty kind="notice" title="Build logs are restricted">
        Your role doesn&apos;t include this deployment&apos;s logs.
      </CloudEmpty>
    )
  return (
    <section aria-label="Logs" className="grid min-w-0 gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <SourceSwitch
          sources={sources}
          value={picked?.id}
          onChange={onSource}
        />
        {attempts.length > 1 && (
          <Segments
            label="Build attempt"
            value={attempt}
            onChange={setAttempt}
            items={[
              { id: "all", label: "All attempts" },
              ...attempts.map((a) => ({ id: a, label: `Attempt ${a}` })),
            ]}
          />
        )}
        <span className="ml-auto text-xs text-muted-foreground">
          {connection.status === "live"
            ? `Live via ${connection.transport.toUpperCase()}`
            : "Status may be stale"}{" "}
          · {output.logs.length} retained lines
        </span>
      </div>
      {output.gap && (
        <CloudCallout
          tone="info"
          icon={InfoIcon}
          title="Some earlier output is gone"
        >
          The visible history keeps the last 2,000 lines.
        </CloudCallout>
      )}
      {!!output.error && (
        <QueryError
          error={output.error}
          fallback="Build output couldn't be loaded."
          retry={output.refetch}
        />
      )}
      {notices(output.collections, picked?.steps?.[0] ?? null).map((n) => (
        <CloudCallout
          key={n.key}
          tone="info"
          icon={InfoIcon}
          title={
            n.unavailable
              ? "Build output unavailable"
              : "Build output is incomplete"
          }
        >
          {n.count > 1 && `For ${n.count} builds. `}
          {n.message}
        </CloudCallout>
      ))}
      {all.length === 0 && output.loaded && !output.error ? (
        <CloudEmpty kind="compact" title="No output recorded yet">
          Build output appears here when the integration supports it.
        </CloudEmpty>
      ) : (
        <LogViewer
          lines={lines}
          live={live && connection.status === "live"}
          name={name}
          step={null}
          onClearStep={() => onSource("all")}
          empty={
            picked?.steps
              ? `Nothing recorded for ${picked.label} yet.`
              : "Waiting for recorded output."
          }
        />
      )}
    </section>
  )
}

/**
 * SourceSwitch picks whose lines the viewer shows. It's the Segments
 * control with each service's state as a dot, so a failed step stands out
 * before it's picked.
 */
function SourceSwitch({
  sources,
  value,
  onChange,
}: {
  sources: LogSource[]
  value?: string
  onChange: (id: string) => void
}) {
  return (
    <div
      role="tablist"
      aria-label="Show lines from"
      className="inline-flex flex-wrap gap-0.5 rounded-[10px] bg-muted p-[3px]"
    >
      {sources.map((s) => {
        const on = s.id === value
        return (
          <button
            key={s.id}
            type="button"
            role="tab"
            aria-selected={on}
            onClick={() => onChange(s.id)}
            className={cn(
              "flex h-7 items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
              on
                ? "bg-background text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            {s.label}
            {s.tone && (
              <span
                aria-hidden="true"
                className={cn("size-1.5 rounded-full", toneClass[s.tone].dot)}
              />
            )}
          </button>
        )
      })}
    </div>
  )
}
