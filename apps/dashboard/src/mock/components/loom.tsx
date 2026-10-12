// The loom thread: a deployment's fifteen steps as one line across the warp.
// Done is overdye green; the running step carries a glowing weld shuttle at
// its head; a plan waiting for approval holds a gold ring; what's left is a
// hairline of warp.
import { Fragment } from "react"

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { stepGroups } from "@/mock/data"
import { duration } from "@/mock/format"
import type { StepState, StepView } from "@/mock/store"

const stateLabel: Record<StepState, string> = {
  done: "Done",
  running: "Running",
  waiting: "Waiting for approval",
  failed: "Failed",
  canceled: "Canceled",
  pending: "Not started",
  skipped: "Skipped",
}

const sizes = {
  sm: { line: "h-[3px]", head: "size-2", ring: "size-2.5" },
  md: { line: "h-[3px]", head: "size-2.5", ring: "size-3" },
  lg: { line: "h-1", head: "size-3", ring: "size-3.5" },
} as const

function Cell({ s, size }: { s: StepView; size: keyof typeof sizes }) {
  const z = sizes[size]
  const line = cn("w-full rounded-full", z.line)
  switch (s.state) {
    case "done":
      return <span className={cn(line, "bg-overdye")} />
    case "failed":
      return (
        <span className="relative flex w-full items-center">
          <span className={cn(line, "bg-destructive")} />
          <span
            className={cn(
              "absolute right-0 rounded-full bg-destructive ring-4 ring-destructive/15",
              z.head
            )}
          />
        </span>
      )
    case "canceled":
      return <span className={cn(line, "bg-muted-foreground/40")} />
    case "waiting":
      return (
        <span className="relative flex w-full items-center">
          <span className={cn(line, "bg-weld/15")} />
          <span
            className={cn(
              "absolute left-0 animate-pulse rounded-full border-2 border-weld bg-background motion-reduce:animate-none",
              z.ring
            )}
          />
        </span>
      )
    case "running": {
      const pct = Math.max((s.progress ?? 0) * 100, 4)
      return (
        <span className="relative flex w-full items-center">
          <span className={cn(line, "bg-weld/20")} />
          <span
            className={cn(
              "absolute left-0 rounded-full bg-weld transition-[width] duration-1000 ease-linear",
              z.line
            )}
            style={{ width: `${pct}%` }}
          />
          <span
            className={cn(
              "shuttle-glow absolute -translate-x-1/2 rounded-full bg-weld transition-[left] duration-1000 ease-linear",
              z.head
            )}
            style={{ left: `${pct}%` }}
          />
        </span>
      )
    }
    case "skipped":
      return <span className="h-px w-full bg-warp opacity-50" />
    case "pending":
      return <span className="h-px w-full bg-warp" />
  }
}

export function LoomThread({
  steps,
  size = "md",
  tooltips = false,
  draw = false,
  className,
}: {
  steps: StepView[]
  size?: keyof typeof sizes
  tooltips?: boolean
  draw?: boolean
  className?: string
}) {
  const current = steps.find((s) => s.state !== "done") ?? steps.at(-1)
  const label = current
    ? `Step ${current.index + 1} of ${steps.length}, ${current.step.name}: ${stateLabel[current.state].toLowerCase()}`
    : "No steps"
  return (
    <div
      role="img"
      aria-label={label}
      className={cn(
        "flex w-full items-center",
        size === "sm" ? "gap-1.5" : "gap-3",
        draw && "loom-draw",
        className
      )}
    >
      {stepGroups.map((g) => {
        const inGroup = steps.filter((s) => s.step.group === g)
        return (
          <div
            key={g}
            className={cn(
              "flex items-center",
              size === "sm" ? "gap-px" : "gap-1"
            )}
            style={{ flexGrow: inGroup.length, flexBasis: 0 }}
          >
            {inGroup.map((s) =>
              tooltips ? (
                <Tooltip key={s.index}>
                  <TooltipTrigger
                    render={<span className="flex flex-1 items-center py-2" />}
                  >
                    <Cell s={s} size={size} />
                  </TooltipTrigger>
                  <TooltipContent>
                    <span className="font-medium">{s.step.name}</span>
                    <span className="ml-2 opacity-70">
                      {stateLabel[s.state]}
                      {s.durationMs !== undefined && (
                        <Fragment> in {duration(s.durationMs)}</Fragment>
                      )}
                    </span>
                  </TooltipContent>
                </Tooltip>
              ) : (
                <span key={s.index} className="flex flex-1 items-center py-1">
                  <Cell s={s} size={size} />
                </span>
              )
            )}
          </div>
        )
      })}
    </div>
  )
}

/** PhaseRuler names the phases under a thread, lined up with its groups. */
export function PhaseRuler({
  steps,
  active,
  className,
}: {
  steps: StepView[]
  active?: string
  className?: string
}) {
  return (
    <div className={cn("flex gap-3 text-xs text-muted-foreground", className)}>
      {stepGroups.map((g) => (
        <span
          key={g}
          style={{
            flexGrow: steps.filter((s) => s.step.group === g).length,
            flexBasis: 0,
          }}
          className={cn(
            "truncate border-l pl-2",
            active === g && "border-foreground/40 font-medium text-foreground"
          )}
        >
          {g}
        </span>
      ))}
    </div>
  )
}

export { stateLabel }
