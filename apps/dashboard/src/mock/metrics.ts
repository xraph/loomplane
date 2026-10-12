// Numbers for the overview's charts. Like data.ts, all made up, but shaped
// to tell a believable story: deploys rising, commit-to-live falling,
// traffic climbing through the day.
import { deployHistory } from "@/mock/data"

const day = 86_400_000

function rng(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function isoWeek(ts: number) {
  const d = new Date(ts)
  d.setHours(0, 0, 0, 0)
  d.setDate(d.getDate() + 3 - ((d.getDay() + 6) % 7))
  const jan4 = new Date(d.getFullYear(), 0, 4)
  return (
    1 +
    Math.round(
      ((d.getTime() - jan4.getTime()) / day - 3 + ((jan4.getDay() + 6) % 7)) / 7
    )
  )
}

/** Deploys per week for the last twelve weeks, oldest first. */
export const weeklyDeploys = Array.from({ length: 12 }, (_, i) => {
  const days = deployHistory.slice(-84).slice(i * 7, i * 7 + 7)
  return {
    label: `W${isoWeek(days[0]?.day ?? Date.now())}`,
    value: days.reduce((a, d) => a + d.succeeded + d.failed + d.canceled, 0),
  }
})

/** Build minutes used this month against the plan's quota. */
export const buildMinutes = { used: 2880, quota: 12000 }

/** p95 latency across every service, this week and last, in ms. */
export const latency = (() => {
  const names = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
  const today = new Date().getDay()
  const labels = Array.from(
    { length: 7 },
    (_, i) => names[(today + 1 + i) % 7] as string
  )
  return {
    labels,
    thisWeek: [92, 88, 95, 84, 79, 86, 81],
    lastWeek: [98, 101, 97, 94, 99, 92, 90],
  }
})()

/** Median commit-to-live in minutes for the last twelve months. */
export const commitToLive = (() => {
  const values = [
    14.2, 12.8, 13.1, 11.4, 10.6, 9.8, 10.1, 8.7, 8.2, 7.4, 6.9, 6.1,
  ]
  const now = new Date()
  return values.map((value, i) => {
    const m = new Date(now.getFullYear(), now.getMonth() - 11 + i, 1)
    return { label: m.toLocaleDateString(undefined, { month: "short" }), value }
  })
})()

export type TrafficRange = "24h" | "7d" | "30d" | "90d"

/** Requests per bucket across every service, with a believable climb. */
export function traffic(range: TrafficRange) {
  const spec = {
    "24h": { points: 48, step: 30 * 60_000, base: 380_000 },
    "7d": { points: 56, step: 3 * 3_600_000, base: 2_300_000 },
    "30d": { points: 60, step: 12 * 3_600_000, base: 9_100_000 },
    "90d": { points: 90, step: day, base: 18_500_000 },
  }[range]
  const r = rng(range.length * 97 + spec.points)
  let v = spec.base
  const now = Date.now()
  const out = Array.from({ length: spec.points }, (_, i) => {
    const trend = 1 + (i / spec.points) * 0.9
    const wave = 1 + 0.18 * Math.sin(i / 3.1) + 0.08 * Math.sin(i / 1.3)
    v = spec.base * trend * wave * (0.9 + r() * 0.2)
    return { t: now - (spec.points - 1 - i) * spec.step, v: Math.round(v) }
  })
  return { points: out, total: out.reduce((a, p) => a + p.v, 0) }
}
