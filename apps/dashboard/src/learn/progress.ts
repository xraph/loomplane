// What someone has seen and done of the walkthrough: the welcome, the tour,
// the getting-started list and each guided lesson. It lives in this
// browser, one record per person per workspace, so a teammate on the same
// machine or another workspace starts fresh.
import { useCallback, useSyncExternalStore } from "react"

export type LessonProgress = {
  /** The step showing now, counted from 0. */
  step: number
  done: boolean
}

export type Progress = {
  /** The welcome dialog was answered or closed. */
  welcomed: boolean
  /** The tour reached its last stop. */
  toured: boolean
  /** The getting-started list is folded away. */
  checklistHidden: boolean
  /** Getting-started tasks ticked by hand; the rest follow real data. */
  ticked: string[]
  lessons: Record<string, LessonProgress>
  /** The lesson open in its panel, kept across reloads. */
  active?: string
}

export const emptyProgress: Progress = {
  welcomed: false,
  toured: false,
  checklistHidden: false,
  ticked: [],
  lessons: {},
}

const prefix = "loomplane.learn.v1"

export function progressKey(userId: string, orgId: string): string {
  return `${prefix}:${userId}:${orgId}`
}

/** parseProgress reads a stored record, keeping what it can of an old or
 * damaged one. */
export function parseProgress(raw: string | null): Progress {
  if (!raw) return emptyProgress
  try {
    const v = JSON.parse(raw) as Partial<Progress>
    const lessons: Record<string, LessonProgress> = {}
    for (const [id, l] of Object.entries(v.lessons ?? {})) {
      if (l && typeof l.step === "number")
        lessons[id] = { step: Math.max(0, l.step), done: !!l.done }
    }
    return {
      welcomed: !!v.welcomed,
      toured: !!v.toured,
      checklistHidden: !!v.checklistHidden,
      ticked: Array.isArray(v.ticked)
        ? v.ticked.filter((t): t is string => typeof t === "string")
        : [],
      lessons,
      active: typeof v.active === "string" ? v.active : undefined,
    }
  } catch {
    return emptyProgress
  }
}

/** tick turns a hand-ticked task on or off. */
export function tick(p: Progress, id: string, on: boolean): Progress {
  const has = p.ticked.includes(id)
  if (on === has) return p
  return {
    ...p,
    ticked: on ? [...p.ticked, id] : p.ticked.filter((t) => t !== id),
  }
}

/** moveLesson puts a lesson at a step, finished or not. */
export function moveLesson(
  p: Progress,
  id: string,
  step: number,
  done = false
): Progress {
  return { ...p, lessons: { ...p.lessons, [id]: { step, done } } }
}

// One cache per key, so every hook reading the same record gets the same
// object back until it changes, as useSyncExternalStore needs.
const cache = new Map<string, { raw: string | null; value: Progress }>()
const listeners = new Set<() => void>()

function read(key: string): Progress {
  let raw: string | null = null
  try {
    raw = window.localStorage.getItem(key)
  } catch {
    // Private windows and blocked storage read as nothing saved.
  }
  const hit = cache.get(key)
  if (hit && hit.raw === raw) return hit.value
  const value = parseProgress(raw)
  cache.set(key, { raw, value })
  return value
}

function write(key: string, value: Progress) {
  const raw = JSON.stringify(value)
  try {
    window.localStorage.setItem(key, raw)
  } catch {
    // Without storage the change still holds until the page reloads.
  }
  cache.set(key, { raw, value })
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  // Another tab of the same workspace moves this one along too.
  const onStorage = (e: StorageEvent) => {
    if (e.key?.startsWith(prefix)) listener()
  }
  window.addEventListener("storage", onStorage)
  return () => {
    listeners.delete(listener)
    window.removeEventListener("storage", onStorage)
  }
}

/** useProgress is one person's walkthrough record in one workspace. */
export function useProgress(
  key: string
): [Progress, (change: (p: Progress) => Progress) => void] {
  const value = useSyncExternalStore(
    subscribe,
    () => read(key),
    () => emptyProgress
  )
  const update = useCallback(
    (change: (p: Progress) => Progress) => write(key, change(read(key))),
    [key]
  )
  return [value, update]
}
