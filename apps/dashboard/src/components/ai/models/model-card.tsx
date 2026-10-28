// One model in the registry: what it is, what it needs, how sure Loomplane
// is that it runs, and the way to deploy it.
import { LockIcon } from "lucide-react"

import type { EntrySummary } from "@/client/forge/loomplane/types"
import { VerificationBadge } from "@/components/ai/models/verification-badge"
import { Button } from "@/components/ui/button"
import { formatContext, formatParams, gpuNeed, taskLabel } from "@/lib/models"
import { cn } from "@/lib/utils"

export function ModelCard({
  entry,
  onDeploy,
  deployBlocked,
  className,
}: {
  entry: EntrySummary
  onDeploy?: () => void
  /** Why Deploy is off for you, such as a missing permission. */
  deployBlocked?: string
  className?: string
}) {
  const facts = [
    formatParams(entry.parameters),
    formatContext(entry.contextLength),
  ]
    .filter(Boolean)
    .join(" · ")
  const need = gpuNeed(entry)
  return (
    <article
      aria-label={entry.title}
      className={cn(
        "flex min-w-0 flex-col gap-3 rounded-xl bg-card p-4 ring-1 ring-border",
        className
      )}
    >
      {/* The badge gets a row of its own, so every card lines it up in
          the same place and long names aren't cut short for it. */}
      <div className="grid min-w-0 gap-2">
        <div className="grid min-w-0 gap-0.5">
          <h3
            className="truncate text-[14.5px] font-semibold"
            title={entry.title}
          >
            {entry.title}
          </h3>
          <p className="truncate text-[12.5px] text-muted-foreground">
            {entry.publisher}
          </p>
        </div>
        <VerificationBadge
          status={entry.verification}
          gpuTitle={entry.testedOn[0]}
        />
      </div>

      {entry.summary && (
        <p
          title={entry.summary}
          className="line-clamp-2 min-h-[2lh] text-[12.5px] leading-relaxed text-muted-foreground"
        >
          {entry.summary}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-1.5">
        {entry.tasks.map((t) => (
          <span
            key={t}
            className="inline-flex h-5 items-center rounded-md bg-muted px-1.5 text-[11.5px] font-medium text-muted-foreground"
          >
            {taskLabel(t)}
          </span>
        ))}
      </div>

      <dl className="grid gap-1 text-[12.5px]">
        <div className="flex min-w-0 gap-2">
          <dt className="sr-only">Size</dt>
          <dd className="font-mono text-[12px] tabular-nums">{facts}</dd>
        </div>
        <div className="flex min-w-0 items-center gap-1.5">
          <dt className="sr-only">License</dt>
          <dd className="flex min-w-0 items-center gap-1.5">
            {entry.gated && (
              <span className="inline-flex shrink-0 items-center gap-1 font-medium">
                <LockIcon aria-hidden="true" className="size-3.5" />
                Gated
              </span>
            )}
            <span
              className="truncate text-muted-foreground"
              title={entry.license.note || entry.license.title}
            >
              {entry.gated && <span aria-hidden="true">· </span>}
              {entry.license.id === "unknown"
                ? "License unknown"
                : entry.license.title}
            </span>
          </dd>
        </div>
      </dl>

      <div className="mt-auto flex flex-wrap items-center justify-between gap-2 pt-1">
        <span className="min-w-0 text-xs text-muted-foreground">{need}</span>
        <Button
          size="sm"
          onClick={onDeploy}
          disabled={!onDeploy || !!deployBlocked}
          title={deployBlocked}
        >
          Deploy
        </Button>
      </div>
    </article>
  )
}
