import { cn } from "@/lib/utils"

/**
 * BrandMark is the logo's mark: two L threads woven into a square. The ink
 * thread follows the text color, so it flips with the theme on its own.
 */
export function BrandMark({
  className,
  inverse,
}: {
  className?: string
  /** On a dark tile: both threads in light shades. */
  inverse?: boolean
}) {
  return (
    <svg
      viewBox="0 0 32 32"
      aria-hidden="true"
      className={cn("size-5 shrink-0", className)}
    >
      <path
        className="fill-current"
        d="M0 10.5L0 21A11 11 0 0 0 11 32L28 32A4 4 0 0 0 28 24L11 24A3 3 0 0 1 8 21L8 10.5Z"
      />
      <path
        className={inverse ? "fill-white/70" : "fill-brand-ink"}
        d="M32 21.5L32 11A11 11 0 0 0 21 0L4 0A4 4 0 0 0 4 8L21 8A3 3 0 0 1 24 11L24 21.5Z"
      />
    </svg>
  )
}
