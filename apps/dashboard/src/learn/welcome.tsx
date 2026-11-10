// The first thing a new workspace shows: the four beats every screen
// follows, and three ways in. Whichever you pick, the getting-started list
// on the Overview keeps your place.
import {
  ArrowRightIcon,
  CompassIcon,
  GraduationCapIcon,
  LayoutGridIcon,
} from "lucide-react"
import { type ReactNode, useState } from "react"

import { BrandMark } from "@/components/brand-mark"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import { cn } from "@/lib/utils"

import type { Lesson } from "./content"

const stages = [
  {
    title: "Plan",
    body: "You pick what to ship. Loomplane works out every change it would make.",
  },
  {
    title: "Review",
    body: "You see each create, update and delete before anything happens.",
  },
  {
    title: "Approve",
    body: "Nothing ships until someone approves the plan.",
  },
  {
    title: "Roll out",
    body: "It deploys into your own cloud accounts and keeps watching.",
  },
]

type Choice = "tour" | "lesson" | "explore"

export function WelcomeDialog({
  lesson,
  onTour,
  onLesson,
  onClose,
}: {
  /** The lesson the guided option starts. */
  lesson: Lesson
  onTour: () => void
  onLesson: () => void
  onClose: () => void
}) {
  const [picked, setPicked] = useState<Choice>("tour")
  const choices: {
    id: Choice
    icon: ReactNode
    title: string
    body: string
    meta: string
    start: string
  }[] = [
    {
      id: "tour",
      icon: <CompassIcon />,
      title: "Take the tour",
      body: "Six stops around the app, so you know where everything lives.",
      meta: "2 min",
      start: "Start the tour",
    },
    {
      id: "lesson",
      icon: <GraduationCapIcon />,
      title: lesson.title,
      body: "A guided lesson that points at each thing to click, in this workspace.",
      meta: `${lesson.minutes} min`,
      start: "Start the lesson",
    },
    {
      id: "explore",
      icon: <LayoutGridIcon />,
      title: "Explore on my own",
      body: "Go straight in. A checklist on the Overview keeps your place.",
      meta: "Any time",
      start: "Go to the Overview",
    },
  ]
  const current = choices.find((c) => c.id === picked) ?? choices[0]
  const start = () => {
    if (picked === "tour") onTour()
    else if (picked === "lesson") onLesson()
    else onClose()
  }
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="gap-0 p-0 sm:max-w-[760px]">
        <div className="flex items-start gap-4 px-6 pt-6">
          <BrandMark className="size-11 shrink-0" />
          <div className="grid gap-1.5">
            <DialogTitle className="text-[22px] leading-tight font-semibold tracking-[-0.02em]">
              Welcome to Loomplane
            </DialogTitle>
            <DialogDescription className="max-w-[56ch] text-sm leading-relaxed">
              Your workspace is ready. Every screen follows the same four
              beats, so it's worth a quick look before you start.
            </DialogDescription>
          </div>
        </div>

        <ol
          aria-label="How a change ships in Loomplane"
          className="mx-6 mt-5 grid gap-1.5 rounded-[14px] bg-muted/60 p-1.5 sm:grid-cols-4"
        >
          {stages.map((s, i) => (
            <li
              key={s.title}
              className="grid content-start gap-1.5 rounded-[10px] bg-card p-3 ring-1 ring-border/60"
            >
              <span className="flex items-center gap-2">
                <span className="flex size-[22px] items-center justify-center rounded-full bg-primary/10 font-mono text-[11.5px] text-primary">
                  {i + 1}
                </span>
                <span className="text-[13.5px] font-semibold">{s.title}</span>
              </span>
              <span className="text-[12.5px] leading-normal text-muted-foreground">
                {s.body}
              </span>
            </li>
          ))}
        </ol>

        <div className="grid gap-2.5 px-6 pt-5">
          <span
            id="welcome-choose"
            className="text-xs font-semibold tracking-[0.06em] text-muted-foreground uppercase"
          >
            How do you want to start?
          </span>
          <div
            role="radiogroup"
            aria-labelledby="welcome-choose"
            className="grid gap-2.5 sm:grid-cols-3"
          >
            {choices.map((c) => {
              const on = c.id === picked
              return (
                <button
                  key={c.id}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  onClick={() => setPicked(c.id)}
                  className={cn(
                    "grid content-start gap-2 rounded-xl border p-3.5 text-left transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    on
                      ? "border-primary/40 bg-primary/5 ring-[3px] ring-primary/10"
                      : "border-border hover:bg-muted/50"
                  )}
                >
                  <span className="flex items-center justify-between">
                    <span
                      className={cn(
                        "flex size-8 items-center justify-center rounded-[9px] [&_svg]:size-4",
                        on ? "bg-background text-primary" : "bg-muted"
                      )}
                    >
                      {c.icon}
                    </span>
                    <span
                      aria-hidden
                      className={cn(
                        "flex size-4 items-center justify-center rounded-full border-[1.5px]",
                        on ? "border-primary" : "border-muted-foreground/50"
                      )}
                    >
                      {on && <span className="size-2 rounded-full bg-primary" />}
                    </span>
                  </span>
                  <span className="text-sm font-semibold">{c.title}</span>
                  <span className="text-[12.5px] leading-normal text-muted-foreground">
                    {c.body}
                  </span>
                  <span className="font-mono text-[11.5px] text-muted-foreground">
                    {c.meta}
                  </span>
                </button>
              )
            })}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2.5 px-6 pt-5 pb-6">
          <span className="flex-1 text-[12.5px] text-muted-foreground">
            You can replay the tour or pick up a lesson later from Help.
          </span>
          <Button variant="ghost" onClick={onClose}>
            Skip for now
          </Button>
          <Button onClick={start}>
            {current.start}
            <ArrowRightIcon />
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
