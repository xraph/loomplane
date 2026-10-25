// Spend, latency and traffic, read from each connected cloud's own billing
// and monitoring. These helpers turn the usage API's reports into what the
// cards say.
import type { Amount, UsageSource } from "@/client/forge/loomplane/types"

/** money writes an amount in its currency, with cents below a hundred. */
export function money(total: number, currency: string): string {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency,
    minimumFractionDigits: total < 100 ? 2 : 0,
    maximumFractionDigits: total < 100 ? 2 : 0,
  }).format(total)
}

/** compact writes a count the short way: 1.2K, 3.4M. */
export const compact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
})

/** change is the percent from before to now, or null with nothing before. */
export function change(now: number, before: number): number | null {
  if (!before) return null
  return Math.round(((now - before) / before) * 100)
}

/**
 * headline picks the currency to lead with, the largest, and keeps the
 * others: accounts billed in different currencies aren't added together.
 */
export function headline(totals: Amount[]): {
  lead: Amount | undefined
  rest: Amount[]
} {
  const sorted = [...totals].sort((a, b) => b.total - a.total)
  return { lead: sorted[0], rest: sorted.slice(1) }
}

/** latencyLabel names a latency's statistic. Azure only reports a mean. */
export function latencyLabel(statistic: string | undefined): string {
  if (statistic === "p95") return "p95"
  if (statistic === "average") return "avg"
  return ""
}

export type UsageNote = {
  key: string
  connectionId: string
  name: string
  reason: string
  /** failed is a cloud that didn't answer, rather than one that can't. */
  failed: boolean
  services: string[]
}

/** notReporting is what to tell someone about the sources that are quiet. */
export function notReporting(sources: UsageSource[]): UsageNote[] {
  const notes = new Map<string, UsageNote>()
  for (const s of sources) {
    if (s.status === "reporting") continue
    const key = `${s.connectionId}:${s.reason ?? ""}`
    const note = notes.get(key) ?? {
      key,
      connectionId: s.connectionId,
      name: s.name || "A removed connection",
      reason: s.reason ?? "",
      failed: s.status === "failed",
      services: [],
    }
    note.services.push(...(s.services ?? []))
    notes.set(key, note)
  }
  return [...notes.values()]
}

const hour = 3_600_000

// Where each range's time axis is named: every six hours across a day,
// then every day, five days or fifteen. Buckets fall on UTC edges.
const tickSpacing: Record<string, { edge: number; every: number }> = {
  "24h": { edge: 6 * hour, every: 1 },
  "7d": { edge: 24 * hour, every: 1 },
  "30d": { edge: 24 * hour, every: 5 },
  "90d": { edge: 24 * hour, every: 15 },
}

/** timeTicks picks the bucket starts a range's axis names. */
export function timeTicks(ts: number[], range: string): number[] {
  const { edge, every } = tickSpacing[range] ?? { edge: 24 * hour, every: 1 }
  return ts.filter((t) => t % edge === 0).filter((_, i) => i % every === 0)
}
