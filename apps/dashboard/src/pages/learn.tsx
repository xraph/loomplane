// Learn: the tour, the guided lessons and the words the app uses, for
// whenever someone wants them after the first visit.
import {
  ChevronRightIcon,
  CompassIcon,
  GraduationCapIcon,
  RotateCcwIcon,
} from "lucide-react"
import { useState } from "react"

import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import {
  concepts,
  type Lesson,
  lessons,
  type Topic,
  topics,
} from "@/learn/content"
import { resumable } from "@/learn/help-menu"
import { useLearn } from "@/learn/learn"

type Filter = Topic | "all"

export function LearnPage() {
  const { progress, startTour, startLesson } = useLearn()
  const [filter, setFilter] = useState<Filter>("all")
  const resume = resumable(progress.lessons)
  const shown = lessons.filter((l) => filter === "all" || l.topic === filter)
  const doneCount =
    lessons.filter((l) => progress.lessons[l.id]?.done).length +
    (progress.toured ? 1 : 0)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Learn" }]} />
      <Page className="gap-7">
        <PageHeading
          title="Learn Loomplane"
          description="Short lessons that run in your real workspace. Each one points at the next thing to click, and nothing ships unless you approve it."
          actions={
            <Button variant="outline" onClick={startTour}>
              <RotateCcwIcon />
              Replay the tour
            </Button>
          }
        />

        {resume && (
          <section
            aria-labelledby="resume-title"
            className="flex flex-wrap items-center gap-x-5 gap-y-3 rounded-2xl bg-primary/[0.04] px-4 py-4 ring-1 ring-primary/15"
          >
            <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-primary text-primary-foreground">
              <GraduationCapIcon className="size-5" />
            </span>
            <div className="grid flex-[1_1_260px] gap-0.5">
              <span className="text-[11.5px] font-semibold tracking-[0.06em] text-primary uppercase">
                Pick up where you left off
              </span>
              <h2
                id="resume-title"
                className="text-base font-semibold tracking-[-0.01em]"
              >
                {resume.lesson.title}
              </h2>
              <span className="text-[13px] text-muted-foreground">
                Step {resume.step + 1} of {resume.lesson.steps.length} ·{" "}
                {resume.lesson.steps[resume.step]?.title}
              </span>
            </div>
            <Button onClick={() => startLesson(resume.lesson.id)}>
              Resume lesson
            </Button>
          </section>
        )}

        <section aria-labelledby="lessons-title" className="grid gap-3.5">
          <div className="flex flex-wrap items-center justify-between gap-2.5">
            <div className="flex items-baseline gap-2.5">
              <h2
                id="lessons-title"
                className="text-base font-semibold tracking-[-0.01em]"
              >
                Guided lessons
              </h2>
              <span className="text-[12.5px] text-muted-foreground">
                {doneCount} of {lessons.length + 1} done
              </span>
            </div>
            <div
              role="group"
              aria-label="Filter lessons by topic"
              className="flex flex-wrap gap-1 rounded-[11px] bg-muted p-[3px]"
            >
              {[{ id: "all" as const, label: "All" }, ...topics].map((t) => {
                const on = filter === t.id
                const count =
                  t.id === "all"
                    ? lessons.length + 1
                    : lessons.filter((l) => l.topic === t.id).length +
                      (t.id === "start" ? 1 : 0)
                return (
                  <button
                    key={t.id}
                    type="button"
                    aria-pressed={on}
                    onClick={() => setFilter(t.id)}
                    className={cn(
                      "flex h-[30px] items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium",
                      on
                        ? "bg-background text-foreground shadow-xs ring-1 ring-border/60"
                        : "text-muted-foreground hover:text-foreground"
                    )}
                  >
                    {t.label}
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {count}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>
          <div className="grid gap-3 sm:grid-cols-[repeat(auto-fill,minmax(min(320px,100%),1fr))]">
            {(filter === "all" || filter === "start") && (
              <LessonCard
                icon={<CompassIcon />}
                title="Take the tour"
                summary="Six stops around the app: where projects, deploys and clouds live."
                meta="2 min · 6 stops"
                topic="Getting started"
                status={{ kind: progress.toured ? "done" : "new" }}
                onStart={startTour}
              />
            )}
            {shown.map((l) => (
              <LessonCard
                key={l.id}
                icon={<GraduationCapIcon />}
                title={l.title}
                summary={l.summary}
                meta={`${l.minutes} min · ${l.steps.length} steps`}
                topic={topics.find((t) => t.id === l.topic)?.label ?? ""}
                status={statusOf(l, progress.lessons[l.id])}
                onStart={() => startLesson(l.id)}
              />
            ))}
          </div>
        </section>

        <section aria-labelledby="concepts-title" className="grid gap-3.5">
          <div className="grid gap-1">
            <h2
              id="concepts-title"
              className="text-base font-semibold tracking-[-0.01em]"
            >
              How Loomplane thinks
            </h2>
            <p className="text-[13px] text-muted-foreground">
              The words you'll see on every page.
            </p>
          </div>
          <dl className="grid gap-x-7 border-t sm:grid-cols-[repeat(auto-fill,minmax(min(320px,100%),1fr))]">
            {concepts.map((c, i) => (
              <div key={c.word} className="flex gap-3 border-b py-3.5">
                <span className="w-6 shrink-0 pt-px font-mono text-[11.5px] text-primary">
                  {String(i + 1).padStart(2, "0")}
                </span>
                <div className="grid min-w-0 gap-0.5">
                  <dt className="text-[13.5px] font-semibold">{c.word}</dt>
                  <dd className="text-[13px] leading-normal text-muted-foreground">
                    {c.body}
                  </dd>
                </div>
              </div>
            ))}
          </dl>
        </section>
      </Page>
    </>
  )
}

type Status =
  | { kind: "new" }
  | { kind: "done" }
  | { kind: "progress"; step: number; of: number }

function statusOf(l: Lesson, at?: { step: number; done: boolean }): Status {
  if (at?.done) return { kind: "done" }
  if (at && at.step > 0)
    return { kind: "progress", step: at.step, of: l.steps.length }
  return { kind: "new" }
}

function LessonCard({
  icon,
  title,
  summary,
  meta,
  topic,
  status,
  onStart,
}: {
  icon: React.ReactNode
  title: string
  summary: string
  meta: string
  topic: string
  status: Status
  onStart: () => void
}) {
  const s = status
  const cta =
    s.kind === "done" ? "Run again" : s.kind === "progress" ? "Resume" : "Start"
  return (
    <button
      type="button"
      onClick={onStart}
      className="group grid content-start gap-2.5 rounded-[14px] bg-card p-4 text-left ring-1 ring-border transition-shadow hover:shadow-sm hover:ring-foreground/15 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
    >
      <span className="flex items-center justify-between gap-2">
        <span
          className={cn(
            "flex size-[34px] items-center justify-center rounded-[10px] [&_svg]:size-4",
            s.kind === "done"
              ? "bg-muted text-muted-foreground"
              : "bg-primary/10 text-primary"
          )}
        >
          {icon}
        </span>
        {s.kind === "done" && (
          <span className="rounded-full bg-overdye-tint px-2 py-0.5 text-xs font-medium text-overdye-ink">
            Done
          </span>
        )}
        {s.kind === "progress" && (
          <span className="rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
            Step {s.step + 1} of {s.of}
          </span>
        )}
      </span>
      <span className="grid gap-1">
        <span className="text-[14.5px] font-semibold tracking-[-0.01em]">
          {title}
        </span>
        <span className="text-[13px] leading-normal text-muted-foreground">
          {summary}
        </span>
      </span>
      <span className="mt-1 flex items-center gap-2.5 border-t pt-2.5 text-[12.5px] text-muted-foreground">
        <span>{topic}</span>
        <span className="font-mono text-[11.5px]">{meta}</span>
        <span className="ml-auto font-medium text-primary">{cta}</span>
        <ChevronRightIcon className="size-3.5 text-primary transition-transform group-hover:translate-x-0.5" />
      </span>
    </button>
  )
}
