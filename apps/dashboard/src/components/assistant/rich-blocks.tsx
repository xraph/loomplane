// The richer blocks in the agent's answers (lib/agent-blocks.ts parses
// them): charts, stat tiles, timelines and callouts.
//
// The charts keep to a few fixed marks so the data is the only loud
// thing: bars no thicker than 24px with a 4px rounded end and a 2px
// surface gap, 2px lines with a ringed end dot, a 10% wash under an area,
// hairline gridlines. Two or more series always get a legend; one series
// is named by the title. Text keeps the text colours, never a series
// colour. Every chart opens as a table too, and copies as CSV.
import {
  ArrowDownRightIcon,
  ArrowUpRightIcon,
  ChartBarIcon,
  CheckCircleIcon,
  InfoIcon,
  LightbulbIcon,
  type Icon,
  MinusCircleIcon,
  TableIcon,
  WarningCircleIcon,
  WarningIcon,
  XCircleIcon,
} from "@phosphor-icons/react"
import { type ReactNode, useState } from "react"
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  LabelList,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts"

import { CopyButton } from "@/components/runs"
import {
  type ChartSpec,
  type Stat,
  type TimelineEvent,
  type Tone,
  formatValue,
} from "@/lib/agent-blocks"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

const toneVar: Record<Exclude<Tone, "neutral">, string> = {
  good: "var(--status-good)",
  warning: "var(--status-warning)",
  serious: "var(--status-serious)",
  critical: "var(--status-critical)",
}

export const toneIcon: Record<Tone, Icon> = {
  good: CheckCircleIcon,
  warning: WarningCircleIcon,
  serious: WarningIcon,
  critical: XCircleIcon,
  neutral: MinusCircleIcon,
}

const toneLabel: Record<Tone, string> = {
  good: "Good",
  warning: "Needs a look",
  serious: "Serious",
  critical: "Critical",
  neutral: "",
}

// The dot beside a status word, in its status colour; the word carries it.
export function ToneDot({
  tone,
  className,
}: {
  tone: Tone
  className?: string
}) {
  return (
    <span
      aria-hidden
      className={cn("inline-block size-2 shrink-0 rounded-full", className)}
      style={{
        background:
          tone === "neutral" ? "var(--muted-foreground)" : toneVar[tone],
      }}
    />
  )
}

/** The colour of each series: its status when it has one, otherwise the
 * next categorical slot, in fixed order. */
function colours(spec: ChartSpec): string[] {
  let slot = 0
  return spec.series.map((s) =>
    s.tone ? toneVar[s.tone] : `var(--series-${(slot++ % 6) + 1})`
  )
}

function csvOf(spec: ChartSpec): string {
  const cell = (v: unknown) => {
    const t = v === null || v === undefined ? "" : String(v)
    return /[",\n]/.test(t) ? `"${t.replace(/"/g, '""')}"` : t
  }
  const head = [spec.x, ...spec.series.map((s) => s.label)].map(cell).join(",")
  const rows = spec.data.map((r) =>
    [r[spec.x], ...spec.series.map((s) => r[s.key])].map(cell).join(",")
  )
  return [head, ...rows].join("\n")
}

export function ChartBlock({ spec }: { spec: ChartSpec }) {
  const [view, setView] = useState<"chart" | "table">("chart")
  const fills = colours(spec)
  const many = spec.series.length > 1
  return (
    <figure className="my-3 grid gap-2 rounded-xl bg-card p-3 ring-1 ring-border">
      <figcaption className="flex items-start gap-2">
        <div className="grid min-w-0 flex-1 gap-0.5">
          {spec.title && (
            <span className="truncate text-[13px] font-semibold">
              {spec.title}
            </span>
          )}
          {spec.subtitle && (
            <span className="text-[12px] text-muted-foreground">
              {spec.subtitle}
            </span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          <ViewToggle view={view} onView={setView} />
          <CopyButton value={csvOf(spec)} label="Copy as CSV" />
        </div>
      </figcaption>
      {many && (
        <ul className="flex flex-wrap gap-x-3 gap-y-1 text-[12px] text-muted-foreground">
          {spec.series.map((s, i) => (
            <li key={s.key} className="flex items-center gap-1.5">
              <span
                aria-hidden
                className={cn(
                  "inline-block shrink-0",
                  spec.type === "line"
                    ? "h-0.5 w-3 rounded-full"
                    : "size-2.5 rounded-[3px]"
                )}
                style={{ background: fills[i] }}
              />
              {s.label}
            </li>
          ))}
        </ul>
      )}
      {view === "chart" ? (
        <Plot spec={spec} fills={fills} />
      ) : (
        <DataTable spec={spec} />
      )}
    </figure>
  )
}

function ViewToggle({
  view,
  onView,
}: {
  view: "chart" | "table"
  onView: (v: "chart" | "table") => void
}) {
  const item = (v: "chart" | "table", label: string, I: Icon) => (
    <button
      type="button"
      aria-pressed={view === v}
      aria-label={label}
      title={label}
      onClick={() => onView(v)}
      className={cn(
        "grid size-6 place-items-center rounded-md text-muted-foreground transition-colors hover:text-foreground",
        view === v && "bg-muted text-foreground"
      )}
    >
      <I className="size-3.5" />
    </button>
  )
  return (
    <div className="flex items-center gap-0.5 rounded-lg p-0.5 ring-1 ring-border">
      {item("chart", "Show as a chart", ChartBarIcon)}
      {item("table", "Show as a table", TableIcon)}
    </div>
  )
}

const axisTick = { fill: "var(--muted-foreground)", fontSize: 11 }

function Plot({ spec, fills }: { spec: ChartSpec; fills: string[] }) {
  const { type, x, series, data, unit } = spec
  const horizontal = type === "hbar"
  const stacked = type === "stacked"
  // A value at each bar's tip only while there are few enough to read.
  const labelBars = !stacked && series.length === 1 && data.length <= 10
  const height = horizontal ? Math.min(40 + data.length * 30, 420) : 220
  const tooltip = (
    <Tooltip
      cursor={
        type === "line" || type === "area"
          ? { stroke: "var(--border)", strokeWidth: 1 }
          : { fill: "var(--muted)", opacity: 0.6 }
      }
      content={(p) => <ChartTip {...p} spec={spec} fills={fills} />}
    />
  )
  const grid = (
    <CartesianGrid
      vertical={horizontal}
      horizontal={!horizontal}
      stroke="var(--border)"
      strokeWidth={1}
    />
  )
  const valueAxis = {
    tick: axisTick,
    tickLine: false,
    axisLine: false,
    width: 48,
    tickCount: 5,
    allowDecimals: false,
    tickFormatter: (v: number) =>
      formatValue(v, unit === "%" ? "%" : undefined, true),
  }
  const categoryAxis = {
    dataKey: x,
    tick: axisTick,
    tickLine: false,
    axisLine: { stroke: "var(--border)" },
    interval: "preserveStartEnd" as const,
    minTickGap: 12,
  }
  const margin = { top: 16, right: 12, bottom: 0, left: 0 }

  let chart: ReactNode
  if (type === "line")
    chart = (
      <LineChart data={data} margin={margin}>
        {grid}
        <XAxis {...categoryAxis} />
        <YAxis {...valueAxis} />
        {tooltip}
        {series.map((s, i) => (
          <Line
            key={s.key}
            dataKey={s.key}
            name={s.label}
            type="monotone"
            stroke={fills[i]}
            strokeWidth={2}
            strokeLinecap="round"
            strokeLinejoin="round"
            connectNulls
            isAnimationActive={false}
            dot={(props: { cx?: number; cy?: number; index?: number }) =>
              props.index === data.length - 1 &&
              props.cx != null &&
              props.cy != null ? (
                <circle
                  key={`end-${s.key}`}
                  cx={props.cx}
                  cy={props.cy}
                  r={4}
                  fill={fills[i]}
                  stroke="var(--card)"
                  strokeWidth={2}
                />
              ) : (
                <g key={`dot-${s.key}-${props.index}`} />
              )
            }
            activeDot={{ r: 5, stroke: "var(--card)", strokeWidth: 2 }}
          />
        ))}
      </LineChart>
    )
  else if (type === "area")
    chart = (
      <AreaChart data={data} margin={margin}>
        {grid}
        <XAxis {...categoryAxis} />
        <YAxis {...valueAxis} />
        {tooltip}
        {series.map((s, i) => (
          <Area
            key={s.key}
            dataKey={s.key}
            name={s.label}
            type="monotone"
            stroke={fills[i]}
            strokeWidth={2}
            fill={fills[i]}
            fillOpacity={0.1}
            connectNulls
            isAnimationActive={false}
            activeDot={{ r: 5, stroke: "var(--card)", strokeWidth: 2 }}
          />
        ))}
      </AreaChart>
    )
  else
    chart = (
      <BarChart
        data={data}
        margin={margin}
        layout={horizontal ? "vertical" : "horizontal"}
        barCategoryGap="28%"
        barGap={2}
      >
        {grid}
        {horizontal ? (
          <>
            <XAxis type="number" {...valueAxis} height={24} />
            <YAxis
              type="category"
              {...categoryAxis}
              axisLine={{ stroke: "var(--border)" }}
              width={96}
              interval={0}
            />
          </>
        ) : (
          <>
            <XAxis {...categoryAxis} />
            <YAxis {...valueAxis} />
          </>
        )}
        {tooltip}
        {series.map((s, i) => {
          const top = !stacked || i === series.length - 1
          const r = 4
          return (
            <Bar
              key={s.key}
              dataKey={s.key}
              name={s.label}
              fill={fills[i]}
              stackId={stacked ? "stack" : undefined}
              maxBarSize={24}
              // The surface gap between touching marks.
              stroke="var(--card)"
              strokeWidth={stacked ? 2 : 0}
              radius={
                top ? (horizontal ? [0, r, r, 0] : [r, r, 0, 0]) : [0, 0, 0, 0]
              }
              isAnimationActive={false}
            >
              {labelBars && (
                <LabelList
                  dataKey={s.key}
                  position={horizontal ? "right" : "top"}
                  fill="var(--muted-foreground)"
                  fontSize={11}
                  formatter={(v: unknown) =>
                    typeof v === "number" ? formatValue(v, unit, true) : ""
                  }
                />
              )}
            </Bar>
          )
        })}
      </BarChart>
    )

  return (
    <div style={{ height }} className="w-full min-w-0 text-[11px]">
      <ResponsiveContainer width="100%" height="100%">
        {chart}
      </ResponsiveContainer>
    </div>
  )
}

function ChartTip({
  active,
  payload,
  label,
  spec,
  fills,
}: {
  active?: boolean
  payload?: readonly { dataKey?: unknown; value?: unknown }[]
  label?: unknown
  spec: ChartSpec
  fills: string[]
}) {
  if (!active || !payload?.length) return null
  return (
    <div className="grid min-w-36 gap-1 rounded-lg bg-popover px-2.5 py-2 text-[12px] text-popover-foreground shadow-md ring-1 ring-border">
      <span className="font-medium">{String(label)}</span>
      {spec.series.map((s, i) => {
        const p = payload.find((x) => x.dataKey === s.key)
        if (!p) return null
        return (
          <span key={s.key} className="flex items-center gap-2">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ background: fills[i] }}
            />
            <span className="text-muted-foreground">{s.label}</span>
            <span className="ml-auto font-mono tabular-nums">
              {typeof p.value === "number"
                ? formatValue(p.value, spec.unit)
                : "–"}
            </span>
          </span>
        )
      })}
    </div>
  )
}

function DataTable({ spec }: { spec: ChartSpec }) {
  return (
    <div className="max-h-72 overflow-auto rounded-lg ring-1 ring-border">
      <table className="w-full text-[12.5px]">
        <thead className="sticky top-0 bg-subtle">
          <tr>
            <th className="border-b px-2.5 py-1.5 text-left font-medium">
              {spec.x}
            </th>
            {spec.series.map((s) => (
              <th
                key={s.key}
                className="border-b px-2.5 py-1.5 text-right font-medium"
              >
                {s.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {spec.data.map((r, i) => (
            <tr key={i} className="even:bg-subtle/50">
              <td className="border-b px-2.5 py-1.5">{r[spec.x]}</td>
              {spec.series.map((s) => (
                <td
                  key={s.key}
                  className="border-b px-2.5 py-1.5 text-right font-mono tabular-nums"
                >
                  {typeof r[s.key] === "number"
                    ? formatValue(r[s.key] as number, spec.unit)
                    : "–"}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** MaybeLink renders a dashboard path through the router, an outside one
 * in a new tab, and nothing for anything else. */
function MaybeLink({
  href,
  className,
  children,
}: {
  href?: string
  className?: string
  children: ReactNode
}) {
  if (href?.startsWith("/") && !href.startsWith("//"))
    return (
      <Link to={href as "/"} className={className}>
        {children}
      </Link>
    )
  if (href && /^https:\/\//.test(href))
    return (
      <a href={href} target="_blank" rel="noreferrer" className={className}>
        {children}
      </a>
    )
  return <span className={className}>{children}</span>
}

export function StatsBlock({ stats }: { stats: Stat[] }) {
  return (
    <div className="my-3 grid grid-cols-[repeat(auto-fit,minmax(9.5rem,1fr))] gap-2">
      {stats.map((s, i) => {
        const tone = s.tone ?? "neutral"
        const ToneIcon = toneIcon[tone]
        const up = s.delta?.trim().startsWith("+")
        const down =
          s.delta?.trim().startsWith("-") || s.delta?.trim().startsWith("−")
        return (
          <MaybeLink
            key={i}
            href={s.href}
            className={cn(
              "grid content-start gap-1 rounded-xl bg-card p-3 ring-1 ring-border",
              s.href && "transition-colors hover:bg-muted/50"
            )}
          >
            <span className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
              {tone !== "neutral" && (
                <ToneIcon
                  weight="fill"
                  aria-label={toneLabel[tone]}
                  className="size-3.5 shrink-0"
                  style={{ color: toneVar[tone] }}
                />
              )}
              <span className="truncate">{s.label}</span>
            </span>
            <span className="font-mono text-[22px] leading-tight font-semibold tracking-tight tabular-nums">
              {s.value}
            </span>
            {s.delta && (
              <span className="flex items-center gap-1 text-[12px] text-muted-foreground">
                {up && <ArrowUpRightIcon className="size-3" />}
                {down && <ArrowDownRightIcon className="size-3" />}
                {s.delta}
              </span>
            )}
            {s.hint && (
              <span className="text-[11.5px] text-muted-foreground">
                {s.hint}
              </span>
            )}
          </MaybeLink>
        )
      })}
    </div>
  )
}

function when(date?: string): string | undefined {
  if (!date) return undefined
  const t = Date.parse(date)
  if (Number.isNaN(t)) return date
  const hasTime = /T\d|\d:\d/.test(date)
  return new Date(t).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    year:
      new Date(t).getFullYear() === new Date().getFullYear()
        ? undefined
        : "numeric",
    ...(hasTime ? { hour: "numeric", minute: "2-digit" } : {}),
  })
}

export function TimelineBlock({ events }: { events: TimelineEvent[] }) {
  return (
    <ol className="my-3 grid gap-0 rounded-xl bg-card px-3 py-2 ring-1 ring-border">
      {events.map((e, i) => {
        const tone = e.tone ?? "neutral"
        return (
          <li
            key={i}
            className="relative grid grid-cols-[auto_1fr] gap-x-3 py-2"
          >
            <span className="relative flex w-3 justify-center">
              <span
                aria-hidden
                className={cn(
                  "absolute top-0 bottom-0 w-px bg-border",
                  i === 0 && "top-2",
                  i === events.length - 1 && "bottom-auto h-2"
                )}
              />
              <span
                className="relative mt-1 size-2.5 rounded-full ring-2 ring-card"
                style={{
                  background:
                    tone === "neutral"
                      ? "var(--muted-foreground)"
                      : toneVar[tone],
                }}
                aria-label={toneLabel[tone] || undefined}
              />
            </span>
            <div className="grid min-w-0 gap-0.5">
              <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <MaybeLink
                  href={e.href}
                  className={cn(
                    "text-[13px] font-medium",
                    e.href &&
                      "underline decoration-border underline-offset-2 hover:decoration-foreground"
                  )}
                >
                  {e.title}
                </MaybeLink>
                {e.tag && (
                  <span className="rounded-full px-1.5 font-mono text-[11px] leading-[18px] text-muted-foreground ring-1 ring-border">
                    {e.tag}
                  </span>
                )}
                {e.date && (
                  <time
                    dateTime={e.date}
                    className="ml-auto shrink-0 text-[11.5px] text-muted-foreground tabular-nums"
                  >
                    {when(e.date)}
                  </time>
                )}
              </div>
              {e.detail && (
                <p className="text-[12.5px] text-muted-foreground">
                  {e.detail}
                </p>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

export type CalloutKind = "note" | "tip" | "important" | "warning" | "caution"

const callouts: Record<
  CalloutKind,
  { icon: Icon; label: string; className: string }
> = {
  note: {
    icon: InfoIcon,
    label: "Note",
    className: "bg-flight-tint/60 text-flight-ink",
  },
  tip: {
    icon: LightbulbIcon,
    label: "Tip",
    className: "bg-overdye-tint/60 text-overdye-ink",
  },
  important: {
    icon: InfoIcon,
    label: "Important",
    className: "bg-brand-tint/70 text-brand-ink",
  },
  warning: {
    icon: WarningIcon,
    label: "Warning",
    className: "bg-weld-tint/70 text-weld-ink",
  },
  caution: {
    icon: WarningCircleIcon,
    label: "Caution",
    className: "bg-destructive/8 text-destructive",
  },
}

export function Callout({
  kind,
  children,
}: {
  kind: CalloutKind
  children: ReactNode
}) {
  const c = callouts[kind]
  return (
    <aside
      role="note"
      className={cn(
        "my-3 grid grid-cols-[auto_1fr] gap-x-2.5 rounded-lg px-3 py-2.5 [&_p]:my-0 [&_p+p]:mt-1.5",
        c.className
      )}
    >
      <c.icon weight="fill" className="mt-0.5 size-4" aria-hidden />
      <div className="min-w-0 text-foreground">
        <span className="sr-only">{c.label}: </span>
        {children}
      </div>
    </aside>
  )
}
