// Small pieces the mock's pages share.
import { CheckIcon, CopyIcon } from "lucide-react"
import { useState } from "react"
import { Area, AreaChart, Bar, BarChart } from "recharts"

import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { type ChartConfig, ChartContainer } from "@/components/ui/chart"
import { Progress } from "@/components/ui/progress"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import {
  type Env,
  type Health,
  person as findPerson,
  provider as findProvider,
  type ProviderId,
} from "@/mock/data"
import type { DeploymentStatus } from "@/mock/store"

/** ProviderGlyph is a cloud's small monochrome tile. */
export function ProviderGlyph({
  id,
  className,
}: {
  id: ProviderId
  className?: string
}) {
  const p = findProvider(id)
  return (
    <span
      title={p.name}
      className={cn(
        "inline-flex size-5 shrink-0 items-center justify-center rounded-[5px] border bg-background text-[9px] leading-none font-semibold tracking-tight text-foreground",
        className
      )}
    >
      {id === "vercel" ? (
        <svg viewBox="0 0 10 10" className="size-2.5" aria-hidden="true">
          <path d="M5 1.2 9.2 8.6H.8Z" className="fill-current" />
        </svg>
      ) : id === "aws" ? (
        "aws"
      ) : id === "gcp" ? (
        "G"
      ) : (
        "R"
      )}
      <span className="sr-only">{p.name}</span>
    </span>
  )
}

const statusText: Record<DeploymentStatus, string> = {
  running: "Deploying",
  waiting: "Needs approval",
  succeeded: "Succeeded",
  failed: "Failed",
  canceled: "Canceled",
}

/** StatusDot is a deployment's state as a short thread end. */
export function StatusDot({
  status,
  className,
}: {
  status: DeploymentStatus
  className?: string
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-block h-1.5 w-3 shrink-0 rounded-full",
        status === "succeeded" && "bg-overdye",
        status === "failed" && "bg-destructive",
        status === "canceled" && "bg-muted-foreground/50",
        status === "running" && "thread-shuttle bg-weld",
        status === "waiting" && "bg-weld/20 ring-1 ring-weld ring-inset",
        className
      )}
    />
  )
}

export function StatusBadge({ status }: { status: DeploymentStatus }) {
  return (
    <Badge variant="outline" className="gap-1.5">
      <StatusDot status={status} />
      {statusText[status]}
    </Badge>
  )
}

export { statusText }

const healthText: Record<Health, string> = {
  healthy: "Healthy",
  degraded: "Degraded",
  deploying: "Deploying",
}

export function HealthDot({ health }: { health: Health }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <span
        aria-hidden="true"
        className={cn(
          "size-1.5 rounded-full",
          health === "healthy" && "bg-overdye",
          health === "degraded" && "bg-weld",
          health === "deploying" &&
            "animate-pulse bg-weld motion-reduce:animate-none"
        )}
      />
      {healthText[health]}
    </span>
  )
}

const envTone: Record<Env, string> = {
  production: "border-foreground/25 text-foreground",
  staging: "text-muted-foreground",
  preview: "text-muted-foreground",
}

export function EnvBadge({ env, className }: { env: Env; className?: string }) {
  return (
    <Badge
      variant="outline"
      className={cn("font-normal capitalize", envTone[env], className)}
    >
      {env}
    </Badge>
  )
}

export function PersonAvatar({
  id,
  className,
}: {
  id: string
  className?: string
}) {
  const p = findPerson(id)
  return (
    <Avatar className={cn("size-5", className)}>
      <AvatarFallback className="text-[9px] font-medium">
        {p.initials}
      </AvatarFallback>
    </Avatar>
  )
}

/** Code is an inline identifier: a SHA, an ID, a path. */
export function Code({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <code
      className={cn(
        "font-mono text-[0.8em] text-muted-foreground tabular-nums",
        className
      )}
    >
      {children}
    </code>
  )
}

export function CopyButton({
  value,
  label = "Copy",
  className,
}: {
  value: string
  label?: string
  className?: string
}) {
  const [copied, setCopied] = useState(false)
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={label}
            className={cn("text-muted-foreground", className)}
            onClick={() => {
              void navigator.clipboard?.writeText(value)
              setCopied(true)
              window.setTimeout(() => setCopied(false), 1400)
            }}
          />
        }
      >
        {copied ? <CheckIcon /> : <CopyIcon />}
      </TooltipTrigger>
      <TooltipContent>{copied ? "Copied" : label}</TooltipContent>
    </Tooltip>
  )
}

const sparkConfig = {
  v: { label: "Value", color: "var(--muted-foreground)" },
} satisfies ChartConfig

/** Sparkline is a trend with no axes, sized by its container. */
export function Sparkline({
  data,
  kind = "area",
  color = "var(--muted-foreground)",
  className,
}: {
  data: number[]
  kind?: "area" | "bar"
  color?: string
  className?: string
}) {
  const rows = data.map((v, i) => ({ i, v }))
  return (
    <ChartContainer
      config={{ ...sparkConfig, v: { label: "Value", color } }}
      className={cn("aspect-auto h-8 w-full", className)}
    >
      {kind === "bar" ? (
        <BarChart data={rows} margin={{ top: 2, bottom: 0, left: 0, right: 0 }}>
          <Bar
            dataKey="v"
            fill="var(--color-v)"
            fillOpacity={0.28}
            radius={2}
            isAnimationActive={false}
          />
        </BarChart>
      ) : (
        <AreaChart
          data={rows}
          margin={{ top: 2, bottom: 2, left: 0, right: 0 }}
        >
          <Area
            dataKey="v"
            type="monotone"
            stroke="var(--color-v)"
            strokeWidth={1.5}
            fill="var(--color-v)"
            fillOpacity={0.08}
            isAnimationActive={false}
          />
        </AreaChart>
      )}
    </ChartContainer>
  )
}

/**
 * Meter is a budget or quota bar. It stays quiet until usage passes 85%,
 * then turns weld gold: something needs a look.
 */
export function Meter({
  value,
  label,
  className,
}: {
  value: number
  label: string
  className?: string
}) {
  return (
    <Progress
      value={Math.min(value, 100)}
      aria-label={label}
      className={cn(
        value > 85
          ? "[&_[data-slot=progress-indicator]]:bg-weld"
          : "[&_[data-slot=progress-indicator]]:bg-foreground/50",
        className
      )}
    />
  )
}

/**
 * segmented makes a toggle group read as a segmented control: the chosen
 * segment in full ink, the rest dimmed, so the choice shows at a glance.
 */
export const segmented =
  "text-muted-foreground aria-pressed:bg-muted aria-pressed:text-foreground dark:aria-pressed:bg-input/50"
