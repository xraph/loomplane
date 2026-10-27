// The deployed parts and recovery evidence of a Fabriq resource.
import { Panel } from "@/components/panel"
import { StatusPill, type Tone } from "@/components/runs"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { ago, type StackState } from "@/lib/fabriq"
import { cn } from "@/lib/utils"
import { useFabriqStack } from "./use-fabriq-stack"

export const stackTones: Record<StackState, Tone> = {
  Running: "live",
  Completed: "quiet",
  Off: "quiet",
  External: "quiet",
  "Not running": "waiting",
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
          <Panel
            key={part.id}
            dashed={part.state === "Off"}
            className="gap-1 px-3 py-2.5"
          >
            <span className="flex items-center justify-between gap-1.5">
              <span className="text-[13px] font-semibold">{part.name}</span>
              <StatusPill tone={stackTones[part.state]}>
                {part.state}
              </StatusPill>
            </span>
            <span className="text-xs text-muted-foreground">{part.detail}</span>
          </Panel>
        ))}
      </div>
    </section>
  )
}
