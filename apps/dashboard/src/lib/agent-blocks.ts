// The richer blocks the agent can put in an answer, as fenced code with a
// JSON body: ```chart, ```stats and ```timeline. The model writes them, so
// everything here reads them leniently and checks them strictly: a field
// of the wrong type is dropped, numbers given as strings are read, and a
// block too broken to draw is reported as unreadable rather than guessed
// at. The prompt that teaches the agent these shapes is chatPrompt in
// internal/agentruns/chat.go; keep the two in step.

/** Tone is a status a stat, series or event carries. It is shown with a
 * word or an icon too, never colour alone. */
export type Tone = "good" | "warning" | "serious" | "critical" | "neutral"

const tones: ReadonlySet<string> = new Set([
  "good",
  "warning",
  "serious",
  "critical",
  "neutral",
])

export type ChartType = "bar" | "hbar" | "stacked" | "line" | "area"

export type ChartSeries = {
  key: string
  label: string
  /** A status the series stands for (failed deployments are "critical"),
   * drawn in that status's colour instead of the next categorical one. */
  tone?: Exclude<Tone, "neutral">
}

export type ChartSpec = {
  type: ChartType
  title?: string
  subtitle?: string
  /** The field of each row the categories or the time axis come from. */
  x: string
  series: ChartSeries[]
  data: Record<string, string | number | null>[]
  /** What the values count, for the axis and the tooltip: "deploys",
   * "ms", "USD", "%". */
  unit?: string
}

export type Stat = {
  label: string
  value: string
  /** A change, as the agent words it: "+2 since last week". */
  delta?: string
  tone?: Tone
  /** A dashboard path the tile opens. */
  href?: string
  hint?: string
}

export type TimelineEvent = {
  title: string
  /** When, as an ISO date or time; shown as written when it isn't one. */
  date?: string
  detail?: string
  href?: string
  tone?: Tone
  /** A short label beside the title, such as a version or environment. */
  tag?: string
}

/** The most series one chart draws, the categorical palette's length.
 * Past it the answer should be a table, or small charts. */
export const MAX_SERIES = 6
/** The most rows one chart draws. */
export const MAX_ROWS = 200

const chartTypes: ReadonlySet<string> = new Set([
  "bar",
  "hbar",
  "stacked",
  "line",
  "area",
])

type Parsed<T> = { ok: true; value: T } | { ok: false; reason: string }

const str = (v: unknown): string | undefined =>
  typeof v === "string" && v.trim() ? v.trim() : undefined

const num = (v: unknown): number | null => {
  if (typeof v === "number") return Number.isFinite(v) ? v : null
  if (typeof v === "string") {
    const n = Number(v.replace(/[,_\s]/g, "").replace(/^\$/, ""))
    return v.trim() && Number.isFinite(n) ? n : null
  }
  return null
}

const toneOf = (v: unknown): Tone | undefined =>
  typeof v === "string" && tones.has(v) ? (v as Tone) : undefined

function json(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

/** parseChart reads a ```chart block's body. */
export function parseChart(text: string): Parsed<ChartSpec> {
  const raw = json(text)
  if (!raw || typeof raw !== "object" || Array.isArray(raw))
    return { ok: false, reason: "not a JSON object" }
  const o = raw as Record<string, unknown>
  const type =
    typeof o.type === "string" && chartTypes.has(o.type) ? o.type : "bar"
  const x = str(o.x)
  if (!x) return { ok: false, reason: "no x field" }
  const rows = Array.isArray(o.data) ? o.data : []
  if (!rows.length) return { ok: false, reason: "no data" }

  const given = Array.isArray(o.series) ? o.series : []
  let series: ChartSeries[] = given.flatMap((s): ChartSeries[] => {
    if (typeof s === "string") return [{ key: s, label: s }]
    if (!s || typeof s !== "object") return []
    const r = s as Record<string, unknown>
    const key = str(r.key)
    if (!key) return []
    const tone = toneOf(r.tone)
    return [
      {
        key,
        label: str(r.label) ?? key,
        ...(tone && tone !== "neutral" ? { tone } : {}),
      },
    ]
  })
  // Without series, every numeric field but x is one.
  if (!series.length) {
    const first = rows.find((r) => r && typeof r === "object") as
      Record<string, unknown> | undefined
    series = Object.keys(first ?? {})
      .filter((k) => k !== x && num(first?.[k]) !== null)
      .map((k) => ({ key: k, label: k }))
  }
  series = series.slice(0, MAX_SERIES)
  if (!series.length) return { ok: false, reason: "no numeric series" }

  const data = rows.slice(0, MAX_ROWS).flatMap((r) => {
    if (!r || typeof r !== "object") return []
    const row = r as Record<string, unknown>
    const label = row[x]
    if (typeof label !== "string" && typeof label !== "number") return []
    const out: Record<string, string | number | null> = { [x]: String(label) }
    for (const s of series) out[s.key] = num(row[s.key])
    return [out]
  })
  if (!data.length) return { ok: false, reason: "no rows with an x value" }

  return {
    ok: true,
    value: {
      type: type as ChartType,
      x,
      series,
      data,
      ...(str(o.title) ? { title: str(o.title) } : {}),
      ...(str(o.subtitle) ? { subtitle: str(o.subtitle) } : {}),
      ...(str(o.unit) ? { unit: str(o.unit) } : {}),
    },
  }
}

const list = (text: string, key: string): unknown[] | undefined => {
  const raw = json(text)
  if (Array.isArray(raw)) return raw
  if (raw && typeof raw === "object") {
    const inner = (raw as Record<string, unknown>)[key]
    if (Array.isArray(inner)) return inner
  }
  return undefined
}

/** parseStats reads a ```stats block: a list of tiles, or {stats: [...]}. */
export function parseStats(text: string): Parsed<Stat[]> {
  const items = list(text, "stats")
  if (!items) return { ok: false, reason: "not a JSON list" }
  const stats = items.slice(0, 8).flatMap((s): Stat[] => {
    if (!s || typeof s !== "object") return []
    const r = s as Record<string, unknown>
    const label = str(r.label)
    const value =
      typeof r.value === "number" ? r.value.toLocaleString() : str(r.value)
    if (!label || value === undefined) return []
    const tone = toneOf(r.tone)
    return [
      {
        label,
        value,
        ...(str(r.delta) ? { delta: str(r.delta) } : {}),
        ...(tone ? { tone } : {}),
        ...(str(r.href) ? { href: str(r.href) } : {}),
        ...(str(r.hint) ? { hint: str(r.hint) } : {}),
      },
    ]
  })
  return stats.length
    ? { ok: true, value: stats }
    : { ok: false, reason: "no tiles" }
}

/** parseTimeline reads a ```timeline block: a list of events, or
 * {events: [...]}, shown in the order given. */
export function parseTimeline(text: string): Parsed<TimelineEvent[]> {
  const items = list(text, "events")
  if (!items) return { ok: false, reason: "not a JSON list" }
  const events = items.slice(0, 50).flatMap((e): TimelineEvent[] => {
    if (!e || typeof e !== "object") return []
    const r = e as Record<string, unknown>
    const title = str(r.title)
    if (!title) return []
    const tone = toneOf(r.tone)
    return [
      {
        title,
        ...(str(r.date) ? { date: str(r.date) } : {}),
        ...(str(r.detail) ? { detail: str(r.detail) } : {}),
        ...(str(r.href) ? { href: str(r.href) } : {}),
        ...(str(r.tag) ? { tag: str(r.tag) } : {}),
        ...(tone ? { tone } : {}),
      },
    ]
  })
  return events.length
    ? { ok: true, value: events }
    : { ok: false, reason: "no events" }
}

/** statusTone reads a table cell that is only a status word, such as
 * "Failed" or "live", as the tone it's shown in; any other cell isn't
 * one. */
export function statusTone(text: string): Tone | undefined {
  const w = text.trim().toLowerCase()
  if (!w || w.length > 24) return undefined
  if (
    /^(live|succeeded|success|successful|healthy|ready|passed|done|completed|answered|running ok|up|active|ok)$/.test(
      w
    )
  )
    return "good"
  if (/^(failed|failure|error|down|unhealthy|blocked|broken|critical)$/.test(w))
    return "critical"
  if (/^(degraded|rolled back|rollback|drifted|serious)$/.test(w))
    return "serious"
  if (
    /^(waiting|pending|queued|paused|needs approval|in progress|deploying|running|planning|warning)$/.test(
      w
    )
  )
    return "warning"
  if (/^(cancelled|canceled|stopped|skipped|none|n\/a|unknown|idle)$/.test(w))
    return "neutral"
  return undefined
}

/** isNumeric says whether a table cell reads as a number, so it aligns
 * right: 1,204 / -3 / $12.40 / 99.9% / 120 ms / 4.2 GB. */
export function isNumeric(text: string): boolean {
  return /^[-+]?[$€£]?\d[\d,]*(\.\d+)?\s*(%|ms|s|m|h|kb|mb|gb|tb|k|m|usd|x)?$/i.test(
    text.trim()
  )
}

/** formatValue writes a chart value for an axis or a tooltip: in full
 * under ten thousand, compact above, with the unit when there is one. */
export function formatValue(v: number, unit?: string, compact = false) {
  const n =
    compact && Math.abs(v) >= 10_000
      ? new Intl.NumberFormat(undefined, {
          notation: "compact",
          maximumFractionDigits: 1,
        }).format(v)
      : v.toLocaleString(undefined, { maximumFractionDigits: 2 })
  if (!unit) return n
  if (unit === "%") return `${n}%`
  if (unit.toUpperCase() === "USD" || unit === "$") return `$${n}`
  return `${n} ${unit}`
}
