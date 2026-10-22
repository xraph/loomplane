// Dot-matrix charts, built on shadcn's Chart (recharts underneath). A grid
// of dots is what a loom's crossings look like up close, so the dashboard
// draws its numbers that way: the primary color for what matters now, quiet
// grey for the rest. Pass tone to draw a status instead, such as failed.
import { useState } from "react"
import {
  Bar,
  BarChart,
  Cell,
  Line,
  LineChart,
  Pie,
  PieChart,
  XAxis,
  YAxis,
} from "recharts"

import {
  type ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { Progress } from "@/components/ui/progress"
import { cn } from "@/lib/utils"

const muted = "color-mix(in oklch, var(--foreground) 15%, transparent)"
const faint = "color-mix(in oklch, var(--foreground) 8%, transparent)"
const pitch = 7 // dot spacing, in px

/** What a chart lights up in: the primary color, or a fixed status color. */
export type ChartTone = "primary" | "live" | "waiting" | "flight" | "failed"

const toneColor: Record<ChartTone, string> = {
  primary: "var(--chart-highlight)",
  live: "var(--overdye)",
  waiting: "var(--weld)",
  flight: "var(--flight)",
  failed: "var(--failed)",
}

type ShapeProps = {
  x?: number
  y?: number
  width?: number
  height?: number
  index?: number
}

/** Weekly deploys as dot columns; the hovered or latest week is gold. */
export function DotBarChart({
  tone = "primary",
  data,
  floor = "quiet",
  className,
}: {
  /** The color the chart lights up in. */
  tone?: ChartTone
  data: { label: string; value: number }[]
  /**
   * Where the bars start: "quiet" just below the quietest week, which makes
   * busy weeks stand out when every week is busy; "zero" for small counts,
   * where a quiet week should look quiet.
   */
  floor?: "quiet" | "zero"
  className?: string
}) {
  const [hover, setHover] = useState<number | null>(null)
  const active = hover ?? data.length - 1
  const config = {
    value: { label: "Deploys", color: toneColor[tone] },
  } satisfies ChartConfig
  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-44 w-full", className)}
    >
      <BarChart
        data={data}
        margin={{ top: 8, left: 0, right: 0, bottom: 0 }}
        onMouseMove={(s) => {
          const i = Number(s?.activeTooltipIndex)
          setHover(Number.isFinite(i) ? i : null)
        }}
        onMouseLeave={() => setHover(null)}
      >
        <XAxis
          dataKey="label"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          interval={0}
          fontSize={11}
        />
        {/* See floor: below the quietest week, or from zero. */}
        <YAxis
          hide
          domain={
            floor === "zero" ? [0, "dataMax"] : ["dataMin - 18", "dataMax"]
          }
        />
        <ChartTooltip
          cursor={false}
          content={<ChartTooltipContent hideIndicator />}
        />
        <Bar
          dataKey="value"
          isAnimationActive={false}
          shape={(p: ShapeProps) => {
            const { x = 0, y = 0, width = 0, height = 0, index = 0 } = p
            const cx = x + width / 2
            const rows = Math.max(1, Math.floor(height / pitch))
            return (
              <g>
                {Array.from({ length: rows }, (_, k) =>
                  [-pitch / 2, pitch / 2].map((dx) => (
                    <circle
                      key={`${k}${dx}`}
                      cx={cx + dx}
                      cy={y + height - pitch / 2 - k * pitch}
                      r={2.1}
                      fill={index === active ? "var(--color-value)" : muted}
                    />
                  ))
                )}
              </g>
            )
          }}
        />
      </BarChart>
    </ChartContainer>
  )
}

/**
 * Daily deploys as dot columns: succeeded dots in overdye with failed ones
 * stacked on top in red, so the status colors read the same everywhere.
 * Hovering a day lifts it and dims the rest.
 */
export function DotStackChart({
  data,
  className,
}: {
  data: { label: string; succeeded: number; failed: number }[]
  className?: string
}) {
  const [hover, setHover] = useState<number | null>(null)
  const rows = data.map((d) => ({ ...d, total: d.succeeded + d.failed }))
  const config = {
    succeeded: { label: "Succeeded", color: toneColor.live },
    failed: { label: "Failed", color: toneColor.failed },
  } satisfies ChartConfig
  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-48 w-full", className)}
    >
      <BarChart
        data={rows}
        margin={{ top: 8, left: 0, right: 0, bottom: 0 }}
        onMouseMove={(st) => {
          const i = Number(st?.activeTooltipIndex)
          setHover(Number.isFinite(i) ? i : null)
        }}
        onMouseLeave={() => setHover(null)}
      >
        <XAxis
          dataKey="label"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          minTickGap={18}
          fontSize={11}
        />
        <YAxis hide domain={[0, (max: number) => Math.max(max, 4)]} />
        <ChartTooltip cursor={false} content={<ChartTooltipContent />} />
        <Bar
          dataKey="succeeded"
          stackId="d"
          fill="var(--color-succeeded)"
          hide
        />
        <Bar dataKey="failed" stackId="d" fill="var(--color-failed)" hide />
        <Bar
          dataKey="total"
          isAnimationActive={false}
          shape={(
            p: ShapeProps & {
              payload?: { succeeded: number; failed: number; total: number }
            }
          ) => {
            const { x = 0, y = 0, width = 0, height = 0, index = 0 } = p
            const d = p.payload ?? { succeeded: 0, failed: 0, total: 0 }
            const cx = x + width / 2
            const wide = width >= pitch * 2
            const cols = wide ? [-pitch / 2, pitch / 2] : [0]
            const bottom = y + height
            const dim = hover !== null && hover !== index
            // An empty day still shows a quiet row of dots.
            if (d.total === 0) {
              return (
                <g>
                  {cols.map((dx) => (
                    <circle
                      key={dx}
                      cx={cx + dx}
                      cy={bottom - pitch / 2}
                      r={2.1}
                      fill={faint}
                    />
                  ))}
                </g>
              )
            }
            const levels = Math.max(1, Math.floor(height / pitch))
            const okLevels = Math.round((d.succeeded / d.total) * levels)
            return (
              <g opacity={dim ? 0.45 : 1}>
                {Array.from({ length: levels }, (_, k) =>
                  cols.map((dx) => (
                    <circle
                      key={`${k}${dx}`}
                      cx={cx + dx}
                      cy={bottom - pitch / 2 - k * pitch}
                      r={2.1}
                      fill={
                        k < okLevels
                          ? "var(--color-succeeded)"
                          : "var(--color-failed)"
                      }
                    />
                  ))
                )}
              </g>
            )
          }}
        />
      </BarChart>
    </ChartContainer>
  )
}

/** Spent against budget: a grey ring of the whole, a gold arc of the part. */
export function DotRingChart({
  tone = "primary",
  value,
  className,
}: {
  /** The color the chart lights up in. */
  tone?: ChartTone
  value: number
  className?: string
}) {
  const config = {
    spent: { label: "Spent", color: toneColor[tone] },
    budget: { label: "Budget", color: muted },
  } satisfies ChartConfig
  const lit = Math.round(Math.min(Math.max(value, 0), 1) * 20)
  const outer = Array.from({ length: 30 }, () => ({ v: 1 }))
  const inner = Array.from({ length: 20 }, (_, i) => ({ v: 1, lit: i < lit }))
  return (
    <ChartContainer
      config={config}
      className={cn("aspect-square h-24", className)}
    >
      <PieChart>
        <Pie
          data={outer}
          dataKey="v"
          innerRadius="88%"
          outerRadius="97%"
          paddingAngle={7}
          cornerRadius={4}
          stroke="none"
          isAnimationActive={false}
        >
          {outer.map((_, i) => (
            <Cell key={i} fill="var(--color-budget)" />
          ))}
        </Pie>
        <Pie
          data={inner}
          dataKey="v"
          innerRadius="58%"
          outerRadius="68%"
          startAngle={90}
          endAngle={-270}
          paddingAngle={9}
          cornerRadius={4}
          stroke="none"
          isAnimationActive={false}
        >
          {inner.map((d, i) => (
            <Cell key={i} fill={d.lit ? "var(--color-spent)" : faint} />
          ))}
        </Pie>
      </PieChart>
    </ChartContainer>
  )
}

/** Two series drawn as dotted lines: this week in gold, last week in grey. */
export function DottedLineChart({
  tone = "primary",
  labels,
  current,
  previous,
  unit = "",
  className,
}: {
  /** The color the chart lights up in. */
  tone?: ChartTone
  labels: string[]
  current: number[]
  previous: number[]
  unit?: string
  className?: string
}) {
  const config = {
    current: { label: "This week", color: toneColor[tone] },
    previous: { label: "Last week", color: muted },
  } satisfies ChartConfig
  const data = labels.map((label, i) => ({
    label,
    current: current[i],
    previous: previous[i],
  }))
  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-32 w-full", className)}
    >
      <LineChart data={data} margin={{ top: 8, left: 4, right: 4, bottom: 0 }}>
        <XAxis
          dataKey="label"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          interval={0}
          fontSize={11}
          padding={{ left: 14, right: 14 }}
        />
        <YAxis hide domain={["dataMin - 6", "dataMax + 4"]} />
        <ChartTooltip
          cursor={false}
          content={
            <ChartTooltipContent
              formatter={(v, name) => (
                <span className="flex w-full justify-between gap-4">
                  <span className="text-muted-foreground">
                    {config[name as keyof typeof config]?.label}
                  </span>
                  <span className="font-medium tabular-nums">
                    {String(v)}
                    {unit}
                  </span>
                </span>
              )}
            />
          }
        />
        {(["previous", "current"] as const).map((k) => (
          <Line
            key={k}
            dataKey={k}
            type="linear"
            stroke={`var(--color-${k})`}
            strokeWidth={k === "current" ? 3.6 : 3}
            strokeDasharray="0 6.5"
            strokeLinecap="round"
            dot={false}
            activeDot={{ r: 3.5, fill: `var(--color-${k})`, stroke: "none" }}
            isAnimationActive={false}
          />
        ))}
      </LineChart>
    </ChartContainer>
  )
}

/** Soft columns: a gold top edge over a fading gold body. */
export function GlowBarChart({
  tone = "primary",
  data,
  label,
  unit = "",
  ticks = "all",
  className,
}: {
  /** The color the chart lights up in. */
  tone?: ChartTone
  data: { label: string; value: number }[]
  label: string
  unit?: string
  /** Label every column, or only the first and last when labels are long. */
  ticks?: "all" | "ends"
  className?: string
}) {
  const ends =
    ticks === "ends" && data.length > 1
      ? [data[0]!.label, data.at(-1)!.label]
      : undefined
  const config = {
    value: { label, color: toneColor[tone] },
  } satisfies ChartConfig
  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-36 w-full", className)}
    >
      <BarChart
        data={data}
        // Room for the end labels, which centre on the outer columns.
        margin={{
          top: 8,
          left: ends ? 12 : 0,
          right: ends ? 12 : 0,
          bottom: 0,
        }}
      >
        <defs>
          <linearGradient id="glow-bar" x1="0" y1="0" x2="0" y2="1">
            <stop
              offset="0%"
              stopColor="var(--color-value)"
              stopOpacity={0.34}
            />
            <stop
              offset="100%"
              stopColor="var(--color-value)"
              stopOpacity={0}
            />
          </linearGradient>
        </defs>
        <XAxis
          dataKey="label"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          interval={0}
          ticks={ends}
          fontSize={11}
        />
        <ChartTooltip
          cursor={{ fill: "var(--muted)", opacity: 0.5 }}
          content={
            <ChartTooltipContent
              hideIndicator
              formatter={(v) => (
                <span className="font-medium tabular-nums">
                  {String(v)}
                  {unit}
                </span>
              )}
            />
          }
        />
        <Bar
          dataKey="value"
          isAnimationActive={false}
          shape={(p: ShapeProps) => {
            const { x = 0, y = 0, width = 0, height = 0 } = p
            return (
              <g>
                <rect
                  x={x + 2}
                  y={y}
                  width={Math.max(width - 4, 0)}
                  height={height}
                  fill="url(#glow-bar)"
                />
                <rect
                  x={x + 2}
                  y={y}
                  width={Math.max(width - 4, 0)}
                  height={2}
                  rx={1}
                  fill="var(--color-value)"
                />
              </g>
            )
          }}
        />
      </BarChart>
    </ChartContainer>
  )
}

/** DotProgress is shadcn's Progress drawn as a field of dots (mock.css). */
export function DotProgress({
  value,
  label,
  size = "lg",
  className,
}: {
  value: number
  label: string
  size?: "sm" | "lg"
  className?: string
}) {
  return (
    <Progress
      value={Math.min(Math.max(value, 0), 1) * 100}
      aria-label={label}
      className={cn(
        "dot-progress",
        size === "sm" && "dot-progress-sm",
        className
      )}
    />
  )
}
