// Dot-matrix charts, built on shadcn's Chart (recharts underneath). A grid
// of dots is what a loom's crossings look like up close, so the overview
// draws its numbers that way: weld gold for what matters now, quiet grey for
// the rest.
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

type ShapeProps = {
  x?: number
  y?: number
  width?: number
  height?: number
  index?: number
}

/** Weekly deploys as dot columns; the hovered or latest week is gold. */
export function DotBarChart({
  data,
  floor = "quiet",
  className,
}: {
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
    value: { label: "Deploys", color: "var(--weld)" },
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

/** Spent against budget: a grey ring of the whole, a gold arc of the part. */
export function DotRingChart({
  value,
  className,
}: {
  value: number
  className?: string
}) {
  const config = {
    spent: { label: "Spent", color: "var(--weld)" },
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
  labels,
  current,
  previous,
  unit = "",
  className,
}: {
  labels: string[]
  current: number[]
  previous: number[]
  unit?: string
  className?: string
}) {
  const config = {
    current: { label: "This week", color: "var(--weld)" },
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
  data,
  label,
  unit = "",
  className,
}: {
  data: { label: string; value: number }[]
  label: string
  unit?: string
  className?: string
}) {
  const config = {
    value: { label, color: "var(--weld)" },
  } satisfies ChartConfig
  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-36 w-full", className)}
    >
      <BarChart data={data} margin={{ top: 8, left: 0, right: 0, bottom: 0 }}>
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
