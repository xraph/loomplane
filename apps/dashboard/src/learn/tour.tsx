// The tour: a few stops on the Overview, each a spotlight on a real part
// of the page. Stops whose element isn't showing, such as the sidebar on a
// phone, are left out rather than pointed at nothing.
import { CheckIcon } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import { Button } from "@/components/ui/button"
import { Kbd, KbdGroup } from "@/components/ui/kbd"
import { Link, useNavigate, usePath } from "@/lib/router"
import { cn } from "@/lib/utils"

import { anchors, type TourStop, tour } from "./content"
import { Spotlight, findAnchor, useAnchorRect } from "./spotlight"

/** visibleStops is the tour as this screen can show it. */
export function visibleStops(
  stops: TourStop[] = tour,
  find: (s: TourStop) => boolean = (s) => !!findAnchor(s.anchor)
): TourStop[] {
  return stops.filter(find)
}

export function Tour({ onEnd }: { onEnd: (finished: boolean) => void }) {
  const path = usePath()
  const navigate = useNavigate()
  const [stops, setStops] = useState<TourStop[] | null>(null)
  const [at, setAt] = useState(0)
  const back = useRef<Element | null>(null)

  // The tour is about the Overview: start there, and give its cards a
  // moment to load before deciding which stops can show.
  useEffect(() => {
    back.current = document.activeElement
    if (path !== "/") void navigate({ to: "/" })
    // Only where the tour starts.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    if (stops || path !== "/") return
    let tries = 0
    const id = window.setInterval(() => {
      tries++
      const ready =
        !!findAnchor(anchors.waiting) && !!findAnchor(anchors.newDeployment)
      if (ready || tries >= 20) {
        window.clearInterval(id)
        setStops(visibleStops())
      }
    }, 100)
    return () => window.clearInterval(id)
  }, [path, stops])

  const stop = stops?.[at]
  const rect = useAnchorRect(stop?.anchor ?? null)
  const done = !!stops && at >= stops.length

  // Each stop brings its element into view, as on a phone most sit below
  // the fold.
  useEffect(() => {
    if (!stop) return
    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches
    findAnchor(stop.anchor)?.scrollIntoView({
      block: "center",
      behavior: still ? "auto" : "smooth",
    })
  }, [stop])

  const end = (finished: boolean) => {
    onEnd(finished)
    const el = back.current
    if (el instanceof HTMLElement) el.focus()
  }

  // The card is a dialog: the shell's own keys stand aside while it's open.
  useEffect(() => {
    if (!stops) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault()
        end(done)
      } else if (e.key === "ArrowRight" && !done) setAt((i) => i + 1)
      else if (e.key === "ArrowLeft") setAt((i) => Math.max(0, i - 1))
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  })

  if (!stops) return null

  if (done || !stop) {
    return (
      <Spotlight target={null} label="Tour finished">
        <span className="flex size-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <CheckIcon className="size-5" />
        </span>
        <h2 className="text-lg font-semibold tracking-[-0.02em]">
          You know your way around
        </h2>
        <p className="text-[13px] leading-relaxed text-muted-foreground">
          The getting-started list on the Overview shows what's left to set
          up. Help in the top bar can replay this tour or walk you through a
          lesson.
        </p>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button autoFocus onClick={() => end(true)}>
            Done
          </Button>
          <Button
            variant="outline"
            nativeButton={false}
            render={<Link to="/learn" onClick={() => end(true)} />}
          >
            Browse lessons
          </Button>
          <Button
            variant="ghost"
            className="ml-auto text-muted-foreground"
            onClick={() => setAt(0)}
          >
            Start over
          </Button>
        </div>
      </Spotlight>
    )
  }

  return (
    <Spotlight target={rect} label={stop.title}>
      <div className="flex items-center justify-between gap-2">
        <span className="text-[11.5px] font-semibold tracking-[0.06em] text-primary uppercase">
          Tour · {at + 1} of {stops.length}
        </span>
        <button
          type="button"
          onClick={() => end(false)}
          className="text-[12.5px] font-medium text-muted-foreground hover:text-foreground hover:underline"
        >
          Skip tour
        </button>
      </div>
      <h2 className="text-base leading-snug font-semibold tracking-[-0.01em]">
        {stop.title}
      </h2>
      <p className="text-[13px] leading-relaxed text-muted-foreground">
        {stop.body}
      </p>
      {stop.keys && (
        <span className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
          Shortcut
          <KbdGroup>
            {stop.keys.map((k) => (
              <Kbd key={k}>{k}</Kbd>
            ))}
          </KbdGroup>
        </span>
      )}
      <div className="mt-1 flex items-center gap-2">
        <div
          role="group"
          aria-label="Tour stops"
          className="flex flex-1 items-center"
        >
          {stops.map((s, i) => (
            <button
              key={s.anchor}
              type="button"
              aria-label={`Stop ${i + 1}: ${s.title}`}
              aria-current={i === at ? "step" : undefined}
              onClick={() => setAt(i)}
              className="flex h-6 w-[18px] items-center justify-center"
            >
              <span
                className={cn(
                  "block h-1.5 rounded-full transition-all",
                  i === at
                    ? "w-4 bg-primary"
                    : i < at
                      ? "w-1.5 bg-muted-foreground/60"
                      : "w-1.5 bg-border"
                )}
              />
            </button>
          ))}
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={at === 0}
          onClick={() => setAt((i) => i - 1)}
        >
          Back
        </Button>
        <Button size="sm" autoFocus onClick={() => setAt((i) => i + 1)}>
          {at === stops.length - 1 ? "Finish" : "Next"}
        </Button>
      </div>
    </Spotlight>
  )
}
