// The getting-started card at the top of the Overview. It folds away when
// asked, and goes away by itself once every task is done.
import { CheckIcon, ChevronDownIcon, PlayIcon } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

import { type Task, useChecklist } from "./checklist"
import { useLearn } from "./learn"
import { tick } from "./progress"

export function GettingStarted() {
  const { progress, update, startLesson } = useLearn()
  const { tasks, loaded } = useChecklist(progress.ticked)
  const firstOpen = tasks.find((t) => !t.done)?.id
  const [open, setOpen] = useState<string | null | undefined>(undefined)
  const shown = open === undefined ? firstOpen : open

  if (!loaded) return null
  const doneCount = tasks.filter((t) => t.done).length
  const total = tasks.length
  if (doneCount === total) return null
  const doneText = `${doneCount} of ${total} done`

  if (progress.checklistHidden)
    return (
      <button
        type="button"
        onClick={() => update((p) => ({ ...p, checklistHidden: false }))}
        className="flex h-11 items-center gap-3 rounded-xl border border-dashed border-border bg-subtle px-3.5 text-left text-[13px] hover:bg-muted/60"
      >
        <span className="font-semibold">Getting started</span>
        <span className="text-muted-foreground">{doneText}</span>
        <span className="ml-auto font-medium text-primary">Show</span>
      </button>
    )

  return (
    <section
      aria-labelledby="getting-started"
      className="overflow-hidden rounded-2xl bg-primary/[0.03] ring-1 ring-primary/15"
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5 px-4 pt-4 pb-3">
        <div className="grid flex-[1_1_240px] gap-0.5">
          <h2
            id="getting-started"
            className="text-base font-semibold tracking-[-0.01em]"
          >
            Getting started
          </h2>
          <span className="text-[13px] text-muted-foreground">{doneText}</span>
        </div>
        <div className="flex items-center gap-2.5">
          <span
            role="progressbar"
            aria-label="Getting started progress"
            aria-valuemin={0}
            aria-valuemax={total}
            aria-valuenow={doneCount}
            className="block h-1.5 w-40 overflow-hidden rounded-full bg-foreground/8"
          >
            <span
              className="block h-full rounded-full bg-primary transition-[width] duration-300"
              style={{ width: `${(doneCount / total) * 100}%` }}
            />
          </span>
          <span className="font-mono text-xs text-muted-foreground tabular-nums">
            {doneCount}/{total}
          </span>
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground"
            onClick={() => update((p) => ({ ...p, checklistHidden: true }))}
          >
            Hide
          </Button>
        </div>
      </div>
      <ol className="grid gap-0.5 px-2 pb-2">
        {tasks.map((t) => (
          <TaskRow
            key={t.id}
            task={t}
            open={shown === t.id}
            onToggle={() => setOpen(shown === t.id ? null : t.id)}
            onTick={(on) => update((p) => tick(p, t.id, on))}
            onLesson={t.lesson ? () => startLesson(t.lesson!, true) : undefined}
          />
        ))}
      </ol>
    </section>
  )
}

function TaskRow({
  task: t,
  open,
  onToggle,
  onTick,
  onLesson,
}: {
  task: Task
  open: boolean
  onToggle: () => void
  onTick: (on: boolean) => void
  onLesson?: () => void
}) {
  const id = `task-${t.id}`
  const check = (
    <span
      className={cn(
        "flex size-[22px] shrink-0 items-center justify-center rounded-full",
        t.done
          ? "bg-primary text-primary-foreground"
          : "border-[1.5px] border-dashed border-muted-foreground/50 bg-background"
      )}
    >
      {t.done && <CheckIcon className="size-3" strokeWidth={3} />}
    </span>
  )
  return (
    <li
      className={cn(
        "rounded-xl",
        open && "bg-card shadow-xs ring-1 ring-border"
      )}
    >
      <div className="flex items-center gap-1">
        {/* Tasks the workspace can answer tick themselves; the rest are
          yours to tick. */}
        {t.manual ? (
          <button
            type="button"
            aria-pressed={t.done}
            aria-label={
              t.done ? `Mark ${t.title} as not done` : `Mark ${t.title} as done`
            }
            onClick={() => onTick(!t.done)}
            className="ml-2 rounded-full p-0.5 hover:bg-muted"
          >
            {check}
          </button>
        ) : (
          <span
            className="ml-2 p-0.5"
            role="img"
            aria-label={t.done ? "Done" : "Not done yet"}
          >
            {check}
          </span>
        )}
        <button
          type="button"
          aria-expanded={open}
          aria-controls={id}
          onClick={onToggle}
          className="flex h-10 min-w-0 flex-1 items-center gap-2.5 rounded-lg px-2.5 text-left hover:bg-muted/50"
        >
          <span
            className={cn(
              "truncate text-[13.5px]",
              t.done ? "text-muted-foreground" : "font-medium"
            )}
          >
            {t.title}
          </span>
          <span className="ml-auto font-mono text-[11.5px] text-muted-foreground">
            {t.done ? "Done" : `${t.minutes} min`}
          </span>
          <ChevronDownIcon
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-180"
            )}
          />
        </button>
      </div>
      {open && (
        <div id={id} className="grid gap-3 pr-3 pb-3.5 pl-12">
          <p className="max-w-[72ch] text-[13px] leading-relaxed text-muted-foreground">
            {t.body}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant={t.done ? "outline" : "default"}
              nativeButton={false}
              render={<Link to={t.action.to} />}
            >
              {t.action.label}
            </Button>
            {onLesson && !t.done && (
              <Button size="sm" variant="outline" onClick={onLesson}>
                <PlayIcon />
                Guide me through it
              </Button>
            )}
          </div>
        </div>
      )}
    </li>
  )
}
