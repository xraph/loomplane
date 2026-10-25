// The dashboard's default primary color. Every button,
// active tab, link and logo tile follows the selection, in light and dark. Status
// colors (live, waiting, in flight, failed) never do.
//
// A build can also pick one without a code change by setting
// VITE_LOOMPLANE_ACCENT to one of ACCENTS. A saved personal selection wins.

export const ACCENTS = ["graphite", "copper", "ocean", "sage", "plum"] as const

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
  accent: Accent = storedAccent(),
  root: HTMLElement = document.documentElement
) {
  root.dataset.accent = accent
}

export const ACCENT_STORAGE_KEY = "lp-accent"
export const ACCENT_EVENT = "loomplane:accent"

export function storedAccent(): Accent {
  try {
    const saved = localStorage.getItem(ACCENT_STORAGE_KEY)
    return isAccent(saved) ? saved : resolveAccent()
  } catch {
    return resolveAccent()
  }
}

export function currentAccent(): Accent {
  const accent = document.documentElement.dataset.accent
  return isAccent(accent) ? accent : storedAccent()
}

export function setAccent(accent: Accent) {
  applyAccent(accent)
  try {
    localStorage.setItem(ACCENT_STORAGE_KEY, accent)
  } catch {
    // The current session can still change color without persistent storage.
  }
  window.dispatchEvent(new Event(ACCENT_EVENT))
}

export function subscribeAccent(onChange: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (
      event.storageArea !== localStorage ||
      (event.key !== null && event.key !== ACCENT_STORAGE_KEY)
    )
      return
    applyAccent()
    onChange()
  }
  window.addEventListener(ACCENT_EVENT, onChange)
  window.addEventListener("storage", onStorage)
  return () => {
    window.removeEventListener(ACCENT_EVENT, onChange)
    window.removeEventListener("storage", onStorage)
  }
}
