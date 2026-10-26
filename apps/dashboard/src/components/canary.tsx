// The canary on a deployment's page: the ramp step by step, what each
// service's health said at every step, and a plain call-out when a person
// has to check the cloud.
import { TriangleAlertIcon } from "lucide-react"

import type {
  Canary,
  CanaryVerdict,
  Release,
} from "@/client/forge/loomplane/types"
import { useDeploymentsCanary } from "@/client/canary"
import { useQuery } from "@/client/hooks"
import { Panel, PanelHead } from "@/components/panel"
import { toneClass } from "@/components/runs"
import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  errorRate,
  holdLabel,
  latency,
  needsAttention,
  releaseSummary,
  statusLabel,
  type StepState,
  stepStates,
} from "@/lib/canary"
import { cn } from "@/lib/utils"

type Tone = keyof typeof toneClass

// Status colours keep their own tokens, never the accent.
const statusTone: Record<string, Tone> = {
  pending: "flight",
  ramping: "flight",
  promoting: "flight",
  promoted: "live",
  reverting: "flight",
  rolled_back: "waiting",
  revert_failed: "failed",
  promote_failed: "failed",
}

const stepTone: Record<StepState["state"], Tone> = {
  pending: "quiet",
  holding: "flight",
  passed: "live",
  failed: "failed",
  inconclusive: "waiting",
}

const stepWord: Record<StepState["state"], string> = {
  pending: "Waiting",
  holding: "Holding",
  passed: "Passed",
  failed: "Failed",
  inconclusive: "Couldn't be judged",
}

const verdictTone: Record<string, Tone> = {
  pass: "live",
  fail: "failed",
  inconclusive: "waiting",
}

function Tinted({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  const t = toneClass[tone]
  return (
    <Badge variant="secondary" className={cn(t.tint, t.ink)}>
      {children}
    </Badge>
  )
}

/** CanarySteps is the ramp as a row of steps, each in its own state. */
function CanarySteps({ states }: { states: StepState[] }) {
  return (
    <ol aria-label="Ramp" className="flex flex-wrap gap-2">
      {states.map((s) => {
        const t = toneClass[stepTone[s.state]]
        return (
          <li
            key={s.weight}
            className="flex min-w-24 flex-col gap-1 rounded-lg px-3 py-2 ring-1 ring-border"
          >
            <span className="font-mono text-[13px] font-medium tabular-nums">
              {s.weight}%
            </span>
            <span
              className={cn("flex items-center gap-1.5 text-[12.5px]", t.ink)}
            >
              <span
                aria-hidden="true"
                className={cn("size-1.5 rounded-full", t.dot)}
              />
              {stepWord[s.state]}
            </span>
          </li>
        )
      })}
    </ol>
  )
}

/** CanaryTimeline lists each service's verdict at every step and attempt. */
export function CanaryTimeline({
  canary,
  verdicts,
}: {
  canary: Canary
  verdicts: CanaryVerdict[]
}) {
  const rows = [...verdicts].sort(
    (a, b) =>
      a.step - b.step ||
      a.attempt - b.attempt ||
      a.service.localeCompare(b.service)
  )
  if (rows.length === 0) {
    return (
      <p className="text-[13px] text-muted-foreground">
        No verdicts yet. The first one lands after the first step has held for{" "}
        {holdLabel(canary.holdSeconds)}.
      </p>
    )
  }
  return (
    <Table aria-label="Verdicts">
      <TableHeader>
        <TableRow>
          <TableHead>Step</TableHead>
          <TableHead>Service</TableHead>
          <TableHead>Verdict</TableHead>
          <TableHead>Errors</TableHead>
          <TableHead>Latency</TableHead>
          <TableHead>Baseline</TableHead>
          <TableHead>Why</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((v) => (
          <TableRow key={v.id}>
            <TableCell className="font-mono text-[12.5px] tabular-nums">
              {v.weight}%
              {v.attempt > 0 && (
                <span className="text-muted-foreground">
                  {" "}
                  check {v.attempt + 1}
                </span>
              )}
            </TableCell>
            <TableCell className="text-[13px]">{v.service}</TableCell>
            <TableCell>
              <Tinted tone={verdictTone[v.verdict] ?? "quiet"}>
                {v.verdict}
              </Tinted>
            </TableCell>
            <TableCell className="font-mono text-[12.5px] tabular-nums">
              {errorRate(v.candidate)}
            </TableCell>
            <TableCell className="font-mono text-[12.5px] tabular-nums">
              {latency(v.candidate)}
            </TableCell>
            <TableCell className="font-mono text-[12.5px] text-muted-foreground tabular-nums">
              {errorRate(v.baseline)} {latency(v.baseline)}
              {v.baselineFallback && " (before the release)"}
            </TableCell>
            <TableCell className="text-[12.5px] whitespace-normal text-muted-foreground">
              {v.reason}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

function NeedsYou({ canary }: { canary: Canary }) {
  return (
    <div
      role="alert"
      className="flex gap-2.5 rounded-lg bg-failed-tint px-3 py-2.5 text-destructive ring-1 ring-failed/30"
    >
      <TriangleAlertIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0"
      />
      <div className="grid gap-0.5 text-[13px]">
        <span className="text-[11.5px] font-semibold tracking-[0.06em] uppercase">
          Needs you
        </span>
        <span>{canary.reason || "Traffic may still be split."}</span>
        <span>
          Check how the service splits traffic in the cloud before you retry.
        </span>
      </div>
    </div>
  )
}

/** CanaryBody is the panel once there's a canary to show. */
function CanaryBody({
  canary,
  verdicts,
}: {
  canary: Canary
  verdicts: CanaryVerdict[]
}) {
  const attention = needsAttention(canary)
  return (
    <div className="mt-3 grid gap-4">
      {attention && <NeedsYou canary={canary} />}
      <CanarySteps states={stepStates(canary, verdicts)} />
      {!attention && canary.reason && (
        <p className="text-[13px] text-muted-foreground">{canary.reason}</p>
      )}
      {canary.services.length > 0 && (
        <p className="text-[12.5px] text-muted-foreground">
          Ramping {canary.services.map((s) => s.name).join(", ")}
        </p>
      )}
      <CanaryTimeline canary={canary} verdicts={verdicts} />
    </div>
  )
}

function CanaryStatus({ canary }: { canary: Canary }) {
  return (
    <Tinted tone={statusTone[canary.status] ?? "quiet"}>
      {statusLabel[canary.status] ?? canary.status}
      {canary.status === "ramping" && ` at ${canary.weight}%`}
    </Tinted>
  )
}

function Fetched({ deploymentId }: { deploymentId: string }) {
  const report = useQuery(useDeploymentsCanary, { path: { id: deploymentId } })
  if (report.data?.canary) {
    return (
      <>
        <PanelHead
          title="Canary"
          aside={<CanaryStatus canary={report.data.canary} />}
        />
        <CanaryBody
          canary={report.data.canary}
          verdicts={report.data.verdicts}
        />
      </>
    )
  }
  return (
    <>
      <PanelHead title="Canary" />
      <p className="mt-2 text-[13px] text-muted-foreground">
        {report.status === "error"
          ? "The canary couldn't be read."
          : report.data
            ? "This deployment ran no canary."
            : "Reading the canary."}
      </p>
    </>
  )
}

/**
 * CanaryPanel shows the plan's release and, once a deployment exists, its
 * ramp. The page mounts it when either has a canary.
 */
export function CanaryPanel({
  deploymentId,
  release,
}: {
  deploymentId?: string
  release?: Release
}) {
  return (
    <Panel aria-label="Canary release">
      {deploymentId ? (
        <Fetched deploymentId={deploymentId} />
      ) : (
        <>
          <PanelHead title="Canary" />
          <p className="mt-2 text-[13px] text-muted-foreground">
            {release ? releaseSummary(release) : "No canary yet."}
          </p>
          <p className="mt-1 text-[12.5px] text-muted-foreground">
            The ramp starts once the plan is approved.
          </p>
        </>
      )}
    </Panel>
  )
}
