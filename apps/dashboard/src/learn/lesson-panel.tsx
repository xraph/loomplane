// A guided lesson's panel: the steps, the one you're on, and a ring around
// what to click. It watches for you to do the step (a click on the ringed
// element, or arriving at the page it leads to) rather than asking you to
// say you did.
import {
  BookOpenIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
} from "lucide-react"
import { useEffect, useLayoutEffect, useRef, useState } from "react"
import { createPortal } from "react-dom"

import { Button } from "@/components/ui/button"
import { Link, useNavigate, usePath } from "@/lib/router"
import { cn } from "@/lib/utils"

import { type Lesson, anchorSelector } from "./content"
import type { LessonProgress } from "./progress"
import { Ring, findAnchor, type Rect, useAnchorRect } from "./spotlight"

// Near counts as over: a panel just above what to click crowds it.
const clearance = 24

function overlaps(a: Rect, b: DOMRect) {
  return !(
    a.left + a.width + clearance < b.left ||
    a.left - clearance > b.right ||
    a.top + a.height + clearance < b.top ||
    a.top - clearance > b.bottom
  )
}

export function LessonPanel({
  lesson,
  at,
  onMove,
  onExit,
}: {
  lesson: Lesson
  at: LessonProgress
  onMove: (step: number) => void
  onExit: () => void
}) {
  const path = usePath()
  const navigate = useNavigate()
  const total = lesson.steps.length
  const done = at.done || at.step >= total
  const step = Math.min(at.step, total - 1)
  const current = lesson.steps[step]
  const rect = useAnchorRect(done ? null : current.anchor)
  const [folded, setFolded] = useState(false)
  const panel = useRef<HTMLElement | null>(null)
  const [up, setUp] = useState(false)

  // A step that lives on another page takes you there as it starts. The
  // router's path, not the address bar's, which carries the workspace.
  const here = useRef(path)
  useEffect(() => {
    here.current = path
  }, [path])
  useEffect(() => {
    if (done || !current.route) return
    const route = current.route
    const id = window.setTimeout(() => {
      if (!findAnchor(current.anchor) && here.current !== route)
        void navigate({ to: route })
    }, 250)
    return () => window.clearTimeout(id)
    // On each new step only, not on every page change within it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lesson.id, step, done])

  // Doing the step moves the lesson on.
  useEffect(() => {
    if (done) return
    const a = current.advance
    if (a.kind === "path") {
      if (a.match.test(path)) onMove(step + 1)
      return
    }
    if (a.kind !== "click") return
    const selector = anchorSelector(current.anchor)
    const hit = (target: EventTarget | null) =>
      target instanceof Element && !!target.closest(selector)
    // After the click has done its own work.
    const later = () => window.setTimeout(() => onMove(step + 1), 0)
    const onClick = (e: MouseEvent) => hit(e.target) && later()
    const onKey = (e: KeyboardEvent) =>
      (e.key === "Enter" || e.key === " ") && hit(e.target) && later()
    document.addEventListener("click", onClick, true)
    document.addEventListener("keydown", onKey, true)
    return () => {
      document.removeEventListener("click", onClick, true)
      document.removeEventListener("keydown", onKey, true)
    }
  }, [done, current, step, path, onMove])

  // The panel sits bottom right, and moves to the top right when what it
  // points at would be underneath it there.
  useLayoutEffect(() => {
    const el = panel.current
    if (!el || !rect) return
    const h = el.getBoundingClientRect().height
    const w = Math.min(340, window.innerWidth - 32)
    const left = window.innerWidth - 16 - w
    const low = new DOMRect(left, window.innerHeight - 48 - h, w, h)
    const high = new DOMRect(left, 64, w, h)
    setUp(overlaps(rect, low) && !overlaps(rect, high))
  }, [rect, folded, step])

  const where = cn(
    "fixed right-4 z-[56] w-[340px] max-w-[calc(100vw-2rem)]",
    up ? "top-16" : "bottom-12"
  )

  if (folded)
    return createPortal(
      <>
        {rect && <Ring target={rect} />}
        <button
          type="button"
          ref={(el) => {
            panel.current = el
          }}
          onClick={() => setFolded(false)}
          className={cn(
            where,
            "flex w-auto items-center gap-2 rounded-full bg-popover px-3.5 py-2 text-[12.5px] font-medium shadow-lg ring-1 ring-border"
          )}
        >
          <BookOpenIcon className="size-3.5 text-primary" />
          {done
            ? `${lesson.title}: done`
            : `${lesson.title} · ${step + 1} of ${total}`}
          <ChevronUpIcon className="size-3.5 text-muted-foreground" />
        </button>
      </>,
      document.body
    )

  return createPortal(
    <>
      {rect && <Ring target={rect} />}
      <aside
        ref={(el) => {
          panel.current = el
        }}
        aria-label="Guided lesson"
        className={cn(
          where,
          "grid gap-3 rounded-[14px] bg-popover p-4 text-popover-foreground shadow-xl ring-1 ring-border"
        )}
      >
        <div className="flex items-center justify-between gap-2">
          <span className="flex items-center gap-1.5 text-[11.5px] font-semibold tracking-[0.06em] text-primary uppercase">
            <BookOpenIcon className="size-3.5" />
            Guided lesson
          </span>
          <span className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label="Fold the lesson away"
              onClick={() => setFolded(true)}
            >
              <ChevronDownIcon />
            </Button>
            <button
              type="button"
              onClick={onExit}
              className="text-[12.5px] font-medium text-muted-foreground hover:text-foreground hover:underline"
            >
              Exit
            </button>
          </span>
        </div>
        <div className="grid gap-2">
          <h2 className="text-base font-semibold tracking-[-0.01em]">
            {lesson.title}
          </h2>
          <span className="flex items-center gap-2.5">
            <span
              role="progressbar"
              aria-label="Lesson progress"
              aria-valuemin={0}
              aria-valuemax={total}
              aria-valuenow={done ? total : step}
              className="block h-[5px] flex-1 overflow-hidden rounded-full bg-muted"
            >
              <span
                className="block h-full rounded-full bg-primary transition-[width] duration-300"
                style={{ width: `${((done ? total : step) / total) * 100}%` }}
              />
            </span>
            <span className="font-mono text-[11.5px] text-muted-foreground tabular-nums">
              {done ? "Done" : `Step ${step + 1} of ${total}`}
            </span>
          </span>
        </div>

        {done ? (
          <div className="grid gap-3">
            <p className="text-[13px] leading-relaxed text-muted-foreground">
              That's the lesson. Everything you did here was real, so it's all
              still in your workspace.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={onExit}>
                Close
              </Button>
              <Button
                size="sm"
                variant="outline"
                nativeButton={false}
                render={<Link to="/learn" onClick={onExit} />}
              >
                More lessons
              </Button>
            </div>
          </div>
        ) : (
          <ol aria-live="polite" className="grid gap-0.5">
            {lesson.steps.map((s, i) => {
              const now = i === step
              const past = i < step
              return (
                <li
                  key={i}
                  className={cn(
                    "rounded-xl px-2.5 py-2",
                    now && "bg-card shadow-xs ring-1 ring-border"
                  )}
                >
                  <div className="flex items-center gap-2.5">
                    <span
                      className={cn(
                        "flex size-[22px] shrink-0 items-center justify-center rounded-full font-mono text-[11px]",
                        past && "bg-primary text-primary-foreground",
                        now &&
                          "bg-primary/10 text-primary ring-[1.5px] ring-primary",
                        !past &&
                          !now &&
                          "text-muted-foreground ring-[1.5px] ring-border"
                      )}
                    >
                      {past ? <CheckIcon className="size-3" /> : i + 1}
                    </span>
                    <span
                      className={cn(
                        "text-[13.5px]",
                        now ? "font-semibold" : "font-medium",
                        past && "text-muted-foreground"
                      )}
                    >
                      {s.title}
                    </span>
                  </div>
                  {now && (
                    <div className="grid gap-2.5 pt-2 pb-1 pl-8">
                      <p className="text-[13px] leading-relaxed text-muted-foreground">
                        {s.body}
                      </p>
                      {!rect && (
                        <p className="text-[12.5px] text-muted-foreground">
                          {s.route && path !== s.route ? (
                            <Link
                              to={s.route}
                              className="font-medium text-primary hover:underline"
                            >
                              Take me there
                            </Link>
                          ) : (
                            "Look for it on this page. It shows once the page has what it needs."
                          )}
                        </p>
                      )}
                      {s.advance.kind === "next" ? (
                        <Button
                          size="sm"
                          className="justify-self-start"
                          onClick={() => onMove(step + 1)}
                        >
                          {s.advance.label}
                        </Button>
                      ) : (
                        s.waiting && (
                          <span className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
                            <span className="size-[7px] animate-pulse rounded-full bg-primary motion-reduce:animate-none" />
                            {s.waiting}
                          </span>
                        )
                      )}
                    </div>
                  )}
                </li>
              )
            })}
          </ol>
        )}
        {!done && step > 0 && (
          <button
            type="button"
            onClick={() => onMove(step - 1)}
            className="justify-self-start text-[12.5px] font-medium text-muted-foreground hover:text-foreground hover:underline"
          >
            Back a step
          </button>
        )}
      </aside>
    </>,
    document.body
  )
}
