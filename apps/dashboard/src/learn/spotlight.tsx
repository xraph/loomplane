// Pointing at a real element: find it by its anchor, follow it as the page
// scrolls or resizes, and place a card beside it with an arrow. The tour
// dims everything else; a lesson only rings what to click next.
import {
  type CSSProperties,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react"
import { createPortal } from "react-dom"

import { cn } from "@/lib/utils"

import { type Anchor, anchorSelector } from "./content"

export type Rect = { top: number; left: number; width: number; height: number }

function visibleRect(el: Element | null): Rect | null {
  if (!el) return null
  const r = el.getBoundingClientRect()
  if (r.width === 0 && r.height === 0) return null
  return { top: r.top, left: r.left, width: r.width, height: r.height }
}

function same(a: Rect | null, b: Rect | null) {
  if (!a || !b) return a === b
  return (
    Math.round(a.top) === Math.round(b.top) &&
    Math.round(a.left) === Math.round(b.left) &&
    Math.round(a.width) === Math.round(b.width) &&
    Math.round(a.height) === Math.round(b.height)
  )
}

/** findAnchor is the first visible element marked with the anchor. */
export function findAnchor(name: Anchor): Element | null {
  const all = document.querySelectorAll(anchorSelector(name))
  for (const el of all) if (visibleRect(el)) return el
  return null
}

/**
 * useAnchorRect follows where an anchor sits in the viewport, or null while
 * it isn't on the page (still loading, or not there at all).
 */
export function useAnchorRect(name: Anchor | null): Rect | null {
  const [rect, setRect] = useState<Rect | null>(null)
  useEffect(() => {
    if (!name) return
    let frame = 0
    let last: Rect | null = null
    const measure = () => {
      frame = 0
      const next = visibleRect(findAnchor(name))
      if (!same(next, last)) {
        last = next
        setRect(next)
      }
    }
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(measure)
    }
    measure()
    // Pages render in pieces, popups open and close, and anything above
    // can grow: watch the document as well as the window.
    const mutations = new MutationObserver(schedule)
    mutations.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ["class", "style", "hidden", "data-tour"],
    })
    const resize = new ResizeObserver(schedule)
    resize.observe(document.documentElement)
    window.addEventListener("scroll", schedule, true)
    window.addEventListener("resize", schedule)
    return () => {
      if (frame) cancelAnimationFrame(frame)
      mutations.disconnect()
      resize.disconnect()
      window.removeEventListener("scroll", schedule, true)
      window.removeEventListener("resize", schedule)
      setRect(null)
    }
  }, [name])
  return name ? rect : null
}

export type Side = "right" | "bottom" | "left" | "top"

export type Placement = {
  side: Side | "center"
  top: number
  left: number
  /** Where the arrow sits along the card's edge, in px. */
  arrow: number
}

const gap = 14
const margin = 8

const clamp = (v: number, lo: number, hi: number) =>
  Math.max(lo, Math.min(v, Math.max(lo, hi)))

/**
 * place puts a card of the given size next to a target, on the first side
 * with room: beside a narrow target near the left edge (the sidebar), else
 * under it, else wherever fits. With no room anywhere it centers.
 */
export function place(
  target: Rect,
  card: { width: number; height: number },
  view: { width: number; height: number }
): Placement {
  const narrowLeft = target.width < 320 && target.left < view.width / 3
  const order: Side[] = narrowLeft
    ? ["right", "bottom", "left", "top"]
    : ["bottom", "top", "left", "right"]
  const fits: Record<Side, boolean> = {
    right: target.left + target.width + gap + card.width + margin <= view.width,
    left: target.left - gap - card.width >= margin,
    bottom:
      target.top + target.height + gap + card.height + margin <= view.height,
    top: target.top - gap - card.height >= margin,
  }
  const side = order.find((s) => fits[s])
  if (!side)
    return {
      side: "center",
      top: (view.height - card.height) / 2,
      left: (view.width - card.width) / 2,
      arrow: 0,
    }
  if (side === "right" || side === "left") {
    const top = clamp(target.top, margin, view.height - card.height - margin)
    const mid = target.top + target.height / 2
    // An arrow near the target's middle, or its top for a tall one.
    const aim = target.height > card.height ? target.top + 28 : mid
    return {
      side,
      top,
      left:
        side === "right"
          ? target.left + target.width + gap
          : target.left - gap - card.width,
      arrow: clamp(aim - top, 16, card.height - 16),
    }
  }
  const left = clamp(
    target.left + target.width / 2 - card.width / 2,
    margin,
    view.width - card.width - margin
  )
  return {
    side,
    left,
    top:
      side === "bottom"
        ? target.top + target.height + gap
        : target.top - gap - card.height,
    arrow: clamp(target.left + target.width / 2 - left, 16, card.width - 16),
  }
}

function arrowStyle(p: Placement): CSSProperties | undefined {
  const at = p.arrow - 6
  switch (p.side) {
    case "right":
      return { left: -6, top: at }
    case "left":
      return { right: -6, top: at }
    case "bottom":
      return { top: -6, left: at }
    case "top":
      return { bottom: -6, left: at }
    default:
      return undefined
  }
}

const pad = 6

/**
 * Spotlight dims the page except the target and shows the card beside it.
 * Clicks outside the card are held, so the page doesn't move on under a
 * stop; Escape is the card's to handle.
 */
export function Spotlight({
  target,
  label,
  children,
  className,
}: {
  /** null dims the whole page and centers the card. */
  target: Rect | null
  label: string
  children: ReactNode
  className?: string
}) {
  const card = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ width: 340, height: 200 })
  useLayoutEffect(() => {
    const el = card.current
    if (!el) return
    const measure = () => {
      const r = el.getBoundingClientRect()
      setSize((s) =>
        Math.round(s.width) === Math.round(r.width) &&
        Math.round(s.height) === Math.round(r.height)
          ? s
          : { width: r.width, height: r.height }
      )
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const view = { width: window.innerWidth, height: window.innerHeight }
  const hole = target && {
    top: target.top - pad,
    left: target.left - pad,
    width: target.width + pad * 2,
    height: target.height + pad * 2,
  }
  const p: Placement = hole
    ? place(hole, size, view)
    : {
        side: "center",
        top: (view.height - size.height) / 2,
        left: (view.width - size.width) / 2,
        arrow: 0,
      }

  return createPortal(
    <div className="fixed inset-0 z-[60]" data-learn-overlay>
      {hole ? (
        <>
          {/* Four panels dim around the hole; a huge box-shadow spread
            doesn't paint everywhere. */}
          {[
            { top: 0, left: 0, right: 0, height: Math.max(0, hole.top) },
            {
              top: hole.top + hole.height,
              left: 0,
              right: 0,
              bottom: 0,
            },
            {
              top: hole.top,
              left: 0,
              width: Math.max(0, hole.left),
              height: hole.height,
            },
            {
              top: hole.top,
              left: hole.left + hole.width,
              right: 0,
              height: hole.height,
            },
          ].map((s, i) => (
            <div key={i} aria-hidden className="absolute bg-black/50" style={s} />
          ))}
          <div
            aria-hidden
            className="pointer-events-none absolute rounded-xl ring-2 ring-primary ring-offset-2 ring-offset-background"
            style={hole}
          />
        </>
      ) : (
        <div aria-hidden className="absolute inset-0 bg-black/50" />
      )}
      <div
        ref={card}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        className={cn(
          "absolute w-[340px] max-w-[calc(100vw-16px)] rounded-[14px] bg-popover p-4 pb-3 text-popover-foreground shadow-2xl ring-1 ring-border transition-[top,left] duration-300 motion-reduce:transition-none",
          className
        )}
        style={{ top: p.top, left: p.left }}
      >
        {p.side !== "center" && (
          <span
            aria-hidden
            className="absolute size-3 rotate-45 bg-popover"
            style={arrowStyle(p)}
          />
        )}
        <div className="relative grid gap-2">{children}</div>
      </div>
    </div>,
    document.body
  )
}

/**
 * Ring marks what to click next without dimming the page, so a lesson
 * stays out of the way of the work it's teaching.
 */
export function Ring({ target }: { target: Rect }) {
  return createPortal(
    <div
      aria-hidden
      data-learn-ring
      className="pointer-events-none fixed z-[55] animate-pulse rounded-xl outline-2 outline-offset-2 outline-primary outline-solid motion-reduce:animate-none"
      style={{
        top: target.top - 4,
        left: target.left - 4,
        width: target.width + 8,
        height: target.height + 8,
      }}
    />,
    document.body
  )
}
