const minute = 60_000
const hour = 60 * minute

/**
 * ago says how long before now a time was: "just now", "2 minutes ago",
 * "1 hour ago", "yesterday", "3 days ago", then the date once it is more
 * than a month back. A time it can't read comes back as it was given.
 */
export function ago(iso: string, now: number): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return iso
  const elapsed = now - then
  if (elapsed < 45_000) return "just now"
  // Round within a bucket, and move up a bucket when rounding reaches its
  // edge: 59.5 minutes is "1 hour ago", 23.6 hours is "yesterday".
  const minutes = Math.max(1, Math.round(elapsed / minute))
  if (minutes < 60) return plural(minutes, "minute")
  const hours = Math.round(elapsed / hour)
  if (hours < 24) return plural(hours, "hour")
  const days = daysBetween(then, now)
  if (days <= 1) return "yesterday"
  if (days < 31) return `${days} days ago`
  return dateOf(then)
}

/**
 * agoDay is ago at the grain of a calendar day, for a thing that happens
 * daily: "today", "yesterday", "3 days ago".
 */
export function agoDay(iso: string, now: number): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return iso
  const days = daysBetween(then, now)
  if (days <= 0) return "today"
  if (days === 1) return "yesterday"
  if (days < 31) return `${days} days ago`
  return dateOf(then)
}

/**
 * createdLabel is when a tenant was created as its table shows it: "Just
 * now" for one made a moment ago, else the date.
 */
export function createdLabel(iso: string, now: number): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return iso
  return now - then < 45_000 ? "Just now" : dateOf(then)
}

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? "" : "s"} ago`
}

// Whole calendar days in the reader's zone, so 23:50 yesterday is one day
// ago at 00:10, not ten minutes.
function daysBetween(then: number, now: number): number {
  const day = (t: number) => {
    const d = new Date(t)
    return Date.UTC(d.getFullYear(), d.getMonth(), d.getDate())
  }
  return Math.round((day(now) - day(then)) / (24 * hour))
}

const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" })

function dateOf(t: number): string {
  return dateFormat.format(new Date(t))
}
