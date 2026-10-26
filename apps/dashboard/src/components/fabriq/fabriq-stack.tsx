// The deployed parts and recovery evidence of a Fabriq resource.
import type { ReactNode } from "react"
import { toneClass, type Tone } from "@/components/runs"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { ago, type StackState } from "@/lib/fabriq"
import { cn } from "@/lib/utils"
import { useFabriqStack } from "./use-fabriq-stack"

export const stackTones: Record<StackState, Tone | "completed"> = {
  Running: "live",
  Completed: "completed",
  Off: "quiet",
  External: "quiet",
  Linked: "quiet",
  "Not running": "waiting",
}

const pillTones = {
  ...toneClass,
  completed: { tint: "bg-secondary", ink: "text-label" },
  quiet: { tint: "bg-secondary", ink: "text-muted-foreground" },
  waiting: { tint: "bg-notice-warning", ink: "text-status-waiting-ink" },
}

/** StatePill is a part's state in its tone: green running, grey done or off. */
export function StatePill({
  tone,
  title,
  children,
}: {
  tone: Tone | "completed"
  title?: string
  children: ReactNode
}) {
  const t = pillTones[tone]
  return (
    <span
      title={title}
      className={cn(
        "inline-flex shrink-0 items-center rounded-full px-[7px] py-px text-[11.5px] leading-[normal] font-medium whitespace-nowrap",
        t.tint,
        t.ink
      )}
    >
      {children}
    </span>
  )
}

export function FabriqStack(props: CatalogPanelProps) {
  const { parts, migrated, now } = useFabriqStack(props)
  return (
    <section aria-label="Stack" className="@container grid gap-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <h2 className="text-[15px] font-semibold">Stack</h2>
        <span className="text-[12.5px] text-muted-foreground">
          Set up in one click
          {migrated && ` · migrations applied ${ago(migrated.updatedAt, now)}`}
        </span>
      </div>
      <div
        className={cn(
          "grid gap-2",
          props.kind === "fabriq-library"
            ? "grid-cols-1 @[420px]:grid-cols-2 @[840px]:grid-cols-4"
            : "grid-cols-[repeat(auto-fill,minmax(200px,1fr))]"
        )}
      >
        {parts.map((part) => (
          <div
            key={part.id}
            className={cn(
              "grid content-start gap-1 rounded-[12px] border border-border-panel px-3 py-2.5",
              part.state === "Off"
                ? "border-dashed bg-surface-muted"
                : "bg-card"
            )}
          >
            <span className="flex items-center justify-between gap-1.5">
              <span className="text-[13px] font-semibold">{part.name}</span>
              <StatePill tone={stackTones[part.state]}>{part.state}</StatePill>
            </span>
            <span className="text-xs leading-[1.45] text-muted-foreground">
              {part.detail}
            </span>
          </div>
        ))}
      </div>
    </section>
  )
}
