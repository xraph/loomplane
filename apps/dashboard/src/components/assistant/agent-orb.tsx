// The agent's mark: a dark glass orb with ribbons of light turning inside
// it, drawn in SVG and moved in CSS (design.css, .agent-orb). Its state
// follows the conversation:
//
//   idle       drifting slowly
//   listening  you're writing to it: brighter, a little quicker
//   thinking   it's on your message with nothing to show yet: a fast spin
//   working    it's running a tool: an orbit ring circles it
//   speaking   its answer is streaming: it breathes as it writes
//   waiting    it asked you something: amber, a slow pulse
//   error      the turn failed: red, and still
import { useId } from "react"

import { cn } from "@/lib/utils"

export type OrbState =
  | "idle"
  | "listening"
  | "thinking"
  | "working"
  | "speaking"
  | "waiting"
  | "error"

/** busyStates are the states in which the agent is on a message. */
export const busyStates: ReadonlySet<OrbState> = new Set([
  "thinking",
  "working",
  "speaking",
])

export function AgentOrb({
  size = 20,
  state = "idle",
  className,
}: {
  /** Its width and height, in pixels. */
  size?: number
  state?: OrbState
  className?: string
}) {
  // Gradient and filter ids must be unique on the page: there can be a
  // dozen orbs at once.
  const id = useId().replace(/:/g, "")
  const ref = (name: string) => `url(#${id}-${name})`
  return (
    <span
      aria-hidden
      data-state={state}
      className={cn("agent-orb", className)}
      style={{ "--orb-size": `${size}px` } as React.CSSProperties}
    >
      <svg viewBox="0 0 100 100" width={size} height={size}>
        <defs>
          <radialGradient id={`${id}-glass`} cx="50%" cy="58%" r="58%">
            <stop offset="0%" stopColor="var(--orb-deep)" />
            <stop offset="62%" stopColor="oklch(0.09 0.035 282)" />
            <stop offset="100%" stopColor="oklch(0.04 0.015 282)" />
          </radialGradient>
          <linearGradient id={`${id}-ribbon-a`} x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stopColor="var(--orb-a)" stopOpacity="0" />
            <stop offset="30%" stopColor="var(--orb-a)" />
            <stop offset="55%" stopColor="oklch(0.99 0.01 280)" />
            <stop offset="78%" stopColor="var(--orb-c)" />
            <stop offset="100%" stopColor="var(--orb-c)" stopOpacity="0" />
          </linearGradient>
          <linearGradient id={`${id}-ribbon-b`} x1="0" y1="1" x2="1" y2="0">
            <stop offset="0%" stopColor="var(--orb-b)" stopOpacity="0" />
            <stop offset="40%" stopColor="var(--orb-b)" />
            <stop offset="65%" stopColor="oklch(0.95 0.04 300)" />
            <stop offset="100%" stopColor="var(--orb-a)" stopOpacity="0" />
          </linearGradient>
          <linearGradient id={`${id}-rim`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="oklch(1 0 0)" stopOpacity="0.55" />
            <stop offset="45%" stopColor="var(--orb-a)" stopOpacity="0.25" />
            <stop offset="100%" stopColor="var(--orb-c)" stopOpacity="0.8" />
          </linearGradient>
          <filter
            id={`${id}-glow`}
            x="-30%"
            y="-30%"
            width="160%"
            height="160%"
          >
            <feGaussianBlur stdDeviation="2.4" result="blur" />
            <feMerge>
              <feMergeNode in="blur" />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
          <filter
            id={`${id}-haze`}
            x="-50%"
            y="-50%"
            width="200%"
            height="200%"
          >
            <feGaussianBlur stdDeviation="9" />
          </filter>
          <clipPath id={`${id}-clip`}>
            <circle cx="50" cy="50" r="48" />
          </clipPath>
        </defs>

        <circle cx="50" cy="50" r="48" fill={ref("glass")} />
        <g clipPath={ref("clip")}>
          <g className="agent-orb__haze" filter={ref("haze")}>
            <circle cx="34" cy="66" r="20" fill="var(--orb-a)" opacity="0.5" />
            <circle cx="68" cy="34" r="16" fill="var(--orb-b)" opacity="0.45" />
          </g>
          <g className="agent-orb__ribbons">
            <g className="agent-orb__ribbon agent-orb__ribbon--a">
              <path
                d="M6 64 C 28 18, 56 96, 94 38"
                fill="none"
                stroke={ref("ribbon-a")}
                strokeWidth="14"
                strokeLinecap="round"
                filter={ref("glow")}
              />
              <path
                d="M10 62 C 30 22, 56 92, 90 40"
                fill="none"
                stroke="oklch(1 0 0)"
                strokeOpacity="0.75"
                strokeWidth="2.2"
                strokeLinecap="round"
              />
            </g>
            <g className="agent-orb__ribbon agent-orb__ribbon--b">
              <path
                d="M8 36 C 38 82, 60 10, 92 62"
                fill="none"
                stroke={ref("ribbon-b")}
                strokeWidth="9"
                strokeLinecap="round"
                filter={ref("glow")}
                opacity="0.85"
              />
            </g>
          </g>
        </g>
        <circle
          className="agent-orb__orbit"
          cx="50"
          cy="50"
          r="44"
          fill="none"
          stroke="var(--orb-c)"
          strokeWidth="2.5"
          strokeDasharray="10 14"
          strokeLinecap="round"
        />
        <circle
          cx="50"
          cy="50"
          r="47"
          fill="none"
          stroke={ref("rim")}
          strokeWidth="1.6"
        />
        <ellipse
          cx="38"
          cy="21"
          rx="17"
          ry="7"
          fill={ref("glint")}
          transform="rotate(-18 38 21)"
        />
      </svg>
    </span>
  )
}
