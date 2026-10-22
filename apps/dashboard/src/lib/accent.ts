// The dashboard's primary color. Change ACCENT to switch it: every button,
// active tab, link and the logo tile follow, in light and dark. Status
// colors (live, waiting, in flight, failed) never do.
//
// A build can also pick one without a code change by setting
// VITE_LOOMPLANE_ACCENT to one of ACCENTS.

export const ACCENTS = ["graphite", "ocean", "plum"] as const

export type Accent = (typeof ACCENTS)[number]

export const ACCENT: Accent = "ocean"

export function isAccent(value: unknown): value is Accent {
  return (
    typeof value === "string" && (ACCENTS as readonly string[]).includes(value)
  )
}

// The accent in effect: the build's override when it names a known accent,
// else ACCENT.
export function resolveAccent(
  override: unknown = import.meta.env.VITE_LOOMPLANE_ACCENT
): Accent {
  return isAccent(override) ? override : ACCENT
}

// Marks <html> with the accent; the tokens in index.css do the rest.
export function applyAccent(
  accent: Accent = resolveAccent(),
  root: HTMLElement = document.documentElement
) {
  root.dataset.accent = accent
}
