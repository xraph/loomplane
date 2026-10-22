import { InfoIcon } from "lucide-react"
import { useMemo, useState } from "react"
import { statusOf, messageOf } from "@/client/errors"
import { notices } from "@/lib/build-notices"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
} from "@/components/ui/empty"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { LogViewer, type LogLine } from "@/components/runs"
import { useBuildLogs } from "@/lib/build-logs"
import { useLiveState } from "@/lib/realtime-state"

export function BuildLogPanel({
  deploymentId,
  activity,
  live,
  name,
  step,
  stepName,
  onClearStep,
}: {
  deploymentId?: string
  activity: LogLine[]
  live: boolean
  name: string
  step: string | null
  stepName?: string
  onClearStep: () => void
}) {
  const output = useBuildLogs(deploymentId)
  const connection = useLiveState()
  const [attempt, setAttempt] = useState("all")
  const [source, setSource] = useState("all")
  const attempts = [...new Set(output.logs.map((line) => line.attempt))]
  const sources = [...new Set(output.logs.map((line) => line.source))]
  const lines = useMemo(
    () =>
      [
        ...(attempt === "all" && source === "all" ? activity : []),
        ...output.logs
          .filter(
            (line) =>
              (attempt === "all" || line.attempt === attempt) &&
              (source === "all" || line.source === source)
          )
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
    [activity, output.logs, attempt, source]
  )
  if (connection.status === "denied" || statusOf(output.error) === 403)
    return (
      <Empty preview={false}>
        <EmptyHeader>
          <EmptyTitle>Build logs are restricted</EmptyTitle>
          <EmptyDescription>
            Your current permissions do not allow this deployment's logs.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  return (
    <div className="grid min-w-0 gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={attempt}
          onValueChange={(value) => value && setAttempt(value)}
        >
          <SelectTrigger size="sm" aria-label="Build attempt">
            <SelectValue>
              {attempt === "all" ? "All attempts" : `Attempt ${attempt}`}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All attempts</SelectItem>
            {attempts.map((value) => (
              <SelectItem key={value} value={value}>
                Attempt {value}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={source}
          onValueChange={(value) => value && setSource(value)}
        >
          <SelectTrigger size="sm" aria-label="Log source">
            <SelectValue>
              {source === "all" ? "All sources and activity" : source}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All sources and activity</SelectItem>
            {sources.map((value) => (
              <SelectItem key={value} value={value}>
                {value}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="text-xs text-muted-foreground">
          {connection.status === "live"
            ? `Live via ${connection.transport.toUpperCase()}`
            : "Status may be stale"}{" "}
          · {output.logs.length} retained lines
        </span>
      </div>
      {output.gap && (
        <Alert>
          <AlertDescription>
            Some earlier output is unavailable. The visible history is bounded
            to 2,000 lines.
          </AlertDescription>
        </Alert>
      )}
      {!!output.error && (
        <Empty preview={false}>
          <EmptyHeader>
            <EmptyTitle>Build output couldn't be loaded</EmptyTitle>
            <EmptyDescription>
              {messageOf(output.error, "Retry to read the retained output.")}
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button size="sm" onClick={() => void output.refetch()}>
              Retry
            </Button>
          </EmptyContent>
        </Empty>
      )}
      {notices(output.collections, step).map((n) => (
        <div
          key={n.key}
          role="note"
          className="flex items-start gap-2.5 rounded-xl bg-subtle px-3.5 py-2.5 text-[13px] ring-1 ring-border"
        >
          <InfoIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <span className="min-w-0">
            <span className="font-medium">
              {n.unavailable
                ? "Build output unavailable"
                : "Build output is incomplete"}
            </span>
            {n.count > 1 && (
              <span className="text-muted-foreground">
                {" "}
                for {n.count} builds
              </span>
            )}
            {n.message ? (
              <span className="text-muted-foreground">. {n.message}</span>
            ) : (
              "."
            )}
          </span>
        </div>
      ))}
      {lines.length === 0 && output.loaded && !output.error ? (
        <Empty preview={false}>
          <EmptyHeader>
            <EmptyTitle>No output recorded yet</EmptyTitle>
            <EmptyDescription>
              Build output appears here when the integration supports it.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <LogViewer
          lines={lines}
          live={live && connection.status === "live"}
          name={name}
          step={step}
          stepName={stepName}
          onClearStep={onClearStep}
          empty="Waiting for recorded output."
        />
      )}
    </div>
  )
}
