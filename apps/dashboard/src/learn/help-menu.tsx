// Help, in the top bar: lessons for the page you're on, a way back into
// the walkthrough, and the keyboard shortcuts it replaces in the bar.
import {
  BookOpenIcon,
  CircleHelpIcon,
  CompassIcon,
  KeyboardIcon,
  PlayIcon,
  SearchIcon,
} from "lucide-react"
import { type ReactNode, useState } from "react"

import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { useNavigate, usePath } from "@/lib/router"
import { cn } from "@/lib/utils"

import { anchor, anchors, type Lesson, lessonById, lessons } from "./content"
import { useLearn } from "./learn"

type Item = {
  label: string
  icon: ReactNode
  meta?: ReactNode
  run: () => void
}

/** lessonsFor are the lessons that start on the page you're on. */
export function lessonsFor(path: string, all: Lesson[] = lessons): Lesson[] {
  return all.filter((l) => l.steps[0]?.route === path)
}

export function HelpMenu({ onShortcuts }: { onShortcuts: () => void }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const path = usePath()
  const navigate = useNavigate()
  const { progress, startTour, startLesson } = useLearn()
  const close = (then: () => void) => () => {
    setOpen(false)
    setQuery("")
    then()
  }

  const here = lessonsFor(path)
  const resume = progress.active ? undefined : resumable(progress.lessons)
  const groups: { label: string; items: Item[] }[] = [
    {
      label: "On this page",
      items: here.map((l) => ({
        label: `Guide me: ${l.title}`,
        icon: <PlayIcon />,
        meta: `${l.minutes} min`,
        run: close(() => startLesson(l.id, true)),
      })),
    },
    {
      label: "Keep learning",
      items: [
        ...(resume
          ? [
              {
                label: `Resume: ${resume.lesson.title}`,
                icon: <PlayIcon />,
                meta: `${resume.step + 1} of ${resume.lesson.steps.length}`,
                run: close(() => startLesson(resume.lesson.id)),
              },
            ]
          : []),
        {
          label: "Replay the tour",
          icon: <CompassIcon />,
          meta: "2 min",
          run: close(startTour),
        },
        {
          label: "Browse lessons and concepts",
          icon: <BookOpenIcon />,
          run: close(() => void navigate({ to: "/learn" })),
        },
      ],
    },
    {
      label: "More",
      items: [
        {
          label: "Keyboard shortcuts",
          icon: <KeyboardIcon />,
          meta: <Kbd>?</Kbd>,
          run: close(onShortcuts),
        },
      ],
    },
  ]
  const q = query.trim().toLowerCase()
  const shown = groups
    .map((g) => ({
      ...g,
      items: g.items.filter((i) => !q || i.label.toLowerCase().includes(q)),
    }))
    .filter((g) => g.items.length > 0)

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setQuery("")
      }}
    >
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Help"
            {...anchor(anchors.help)}
          />
        }
      >
        <CircleHelpIcon />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[380px] gap-0 p-0">
        <div className="p-3 pb-2">
          <label className="flex h-9 items-center gap-2 rounded-[10px] border bg-subtle px-2.5 text-muted-foreground focus-within:ring-2 focus-within:ring-ring/40">
            <SearchIcon className="size-4" />
            <span className="sr-only">Search help</span>
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search help and lessons"
              className="h-8 min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none placeholder:text-muted-foreground"
            />
          </label>
        </div>
        {shown.map((g) => (
          <div key={g.label} className="grid border-t px-1.5 pt-1 pb-1.5">
            <span className="px-2 pt-2 pb-1 text-[11.5px] font-semibold tracking-[0.06em] text-muted-foreground uppercase">
              {g.label}
            </span>
            {g.items.map((i) => (
              <button
                key={i.label}
                type="button"
                onClick={i.run}
                className="flex min-h-9 items-center gap-2.5 rounded-lg px-2 py-1.5 text-left text-[13px] font-medium hover:bg-muted focus-visible:bg-muted focus-visible:outline-none"
              >
                <span className="flex size-[26px] shrink-0 items-center justify-center rounded-[7px] bg-primary/10 text-primary [&_svg]:size-3.5">
                  {i.icon}
                </span>
                <span className="min-w-0 flex-1 truncate">{i.label}</span>
                {i.meta && (
                  <span className="shrink-0 text-xs font-normal text-muted-foreground">
                    {i.meta}
                  </span>
                )}
              </button>
            ))}
          </div>
        ))}
        {shown.length === 0 && (
          <div className="grid gap-1 border-t px-4 py-5 text-center">
            <span className="text-[13px] font-semibold">
              Nothing matches that
            </span>
            <span className="text-[12.5px] text-muted-foreground">
              Try a word like deploy, project or tour.
            </span>
          </div>
        )}
        <div
          className={cn(
            "flex items-center gap-2 border-t bg-subtle px-3.5 py-2.5 text-xs text-muted-foreground"
          )}
        >
          <span className="flex-1">
            Press <Kbd>?</Kbd> for shortcuts
          </span>
        </div>
      </PopoverContent>
    </Popover>
  )
}

/** resumable is the lesson you started most recently and haven't finished. */
export function resumable(
  at: Record<string, { step: number; done: boolean }>
): { lesson: Lesson; step: number } | undefined {
  for (const [id, p] of Object.entries(at).reverse()) {
    const lesson = lessonById(id)
    if (lesson && !p.done && p.step > 0) return { lesson, step: p.step }
  }
  return undefined
}
