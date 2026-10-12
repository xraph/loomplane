// The weave: a project's environments drawn as weft threads crossing the
// loom's warp. A thread still being set up is weld yellow and runs through
// the warp like a shuttle; a ready one is overdyed green and lies still.
// Under prefers-reduced-motion nothing moves (index.css).
import type { CSSProperties } from "react"

import { cn } from "@/lib/utils"

export type WeftRow = { name: string; ready: boolean }

const rowHeight = 44
const warpGap = 18
const warpWidth = 6
const weftHeight = 12
const width = 720

export function Weave({
  rows,
  label,
  className,
}: {
  rows: WeftRow[]
  label: string
  className?: string
}) {
  const height = rows.length * rowHeight
  const warps = Math.floor(width / warpGap)
  const warpX = (i: number) => i * warpGap + warpGap / 2 - warpWidth / 2

  return (
    <figure
      className={cn(
        "grid grid-cols-[8rem_1fr] items-stretch gap-x-4",
        className
      )}
      aria-label={label}
    >
      <ul className="flex flex-col">
        {rows.map((r) => (
          <li
            key={r.name}
            className="flex flex-col justify-center"
            style={{ height: rowHeight }}
          >
            <span className="text-sm font-medium">{r.name}</span>
            <span
              className={cn(
                "text-xs",
                r.ready ? "text-overdye" : "text-muted-foreground"
              )}
            >
              {r.ready ? "Ready" : "Setting up"}
            </span>
          </li>
        ))}
      </ul>
      <svg
        className="h-full w-full"
        style={{ height }}
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        {Array.from({ length: warps }, (_, i) => (
          <rect
            key={`w${i}`}
            className="weave-warp"
            x={warpX(i)}
            y={0}
            width={warpWidth}
            height={height}
            rx={3}
          />
        ))}
        {rows.map((r, row) => {
          const y = row * rowHeight + rowHeight / 2 - weftHeight / 2
          return (
            <g
              key={r.name}
              className="weave-weft"
              data-ready={r.ready}
              style={{ "--row": row } as CSSProperties}
            >
              <g className="weave-shuttle">
                <rect
                  className="weave-thread"
                  x={0}
                  y={y}
                  width={width}
                  height={weftHeight}
                  rx={6}
                />
                {/* Plain weave: the warp passes over every other crossing,
                    offset by one on each row. */}
                {Array.from({ length: warps }, (_, i) =>
                  (i + row) % 2 === 0 ? (
                    <rect
                      key={`o${i}`}
                      className="weave-over"
                      x={warpX(i)}
                      y={y - 3}
                      width={warpWidth}
                      height={weftHeight + 6}
                      rx={3}
                    />
                  ) : null
                )}
              </g>
            </g>
          )
        })}
      </svg>
    </figure>
  )
}

/** Mark is Loomplane's woven square: three warp threads, three weft. */
export function Mark({ className }: { className?: string }) {
  const cells = [0, 1, 2]
  return (
    <svg
      viewBox="0 0 30 30"
      aria-hidden="true"
      className={cn("size-7 shrink-0", className)}
    >
      {cells.map((i) => (
        <rect
          key={`v${i}`}
          x={3 + i * 9}
          y={1}
          width={6}
          height={28}
          rx={3}
          className="fill-current"
        />
      ))}
      {cells.map((row) => (
        <g key={`h${row}`}>
          <rect
            x={1}
            y={3 + row * 9}
            width={28}
            height={6}
            rx={3}
            className="fill-weld"
          />
          {cells.map((i) =>
            (i + row) % 2 === 0 ? (
              <rect
                key={i}
                x={3 + i * 9}
                y={2 + row * 9}
                width={6}
                height={8}
                rx={3}
                className="fill-current"
              />
            ) : null
          )}
        </g>
      ))}
    </svg>
  )
}
