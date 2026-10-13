// The pieces every deployment view draws a run with. The loom thread is
// the run's steps as one line across the warp: done is overdye green, the
// running step carries a glowing weld shuttle at its head, a plan waiting
// for approval holds a gold ring, and what's left is a hairline of warp.
import {
  ArrowDownIcon,
  CheckIcon,
  CopyIcon,
  DownloadIcon,
  SearchIcon,
  XIcon,
} from "lucide-react"
import {
  Fragment,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Switch } from "@/components/ui/switch"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { clock, duration } from "@/lib/format"
import {
  type RunStatus,
  runStatusText,
  type ThreadState,
  type ThreadStep,
} from "@/lib/runs"
import { cn } from "@/lib/utils"

const stateLabel: Record<ThreadState, string> = {
  done: "Done",
  running: "Running",
  waiting: "Waiting for approval",
  failed: "Failed",
  canceled: "Canceled",
  pending: "Not started",
  skipped: "Skipped",
}

const sizes = {
  sm: { line: "h-[3px]", head: "size-2", ring: "size-2.5" },
  md: { line: "h-[3px]", head: "size-2.5", ring: "size-3" },
  lg: { line: "h-1", head: "size-3", ring: "size-3.5" },
} as const

function Cell({ s, size }: { s: ThreadStep; size: keyof typeof sizes }) {
  const z = sizes[size]
  const line = cn("w-full rounded-full", z.line)
  switch (s.state) {
    case "done":
      return <span className={cn(line, "bg-overdye")} />
    case "failed":
      return (
        <span className="relative flex w-full items-center">
          <span className={cn(line, "bg-destructive")} />
          <span
            className={cn(
              "absolute right-0 rounded-full bg-destructive ring-4 ring-destructive/15",
              z.head
            )}
          />
        </span>
      )
    case "canceled":
      return <span className={cn(line, "bg-muted-foreground/40")} />
    case "waiting":
      return (
        <span className="relative flex w-full items-center">
          <span className={cn(line, "bg-weld/15")} />
          <span
            className={cn(
              "absolute left-0 animate-pulse rounded-full border-2 border-weld bg-background motion-reduce:animate-none",
              z.ring
            )}
          />
        </span>
      )
    case "running": {
      const pct = Math.max((s.progress ?? 0) * 100, 4)
      return (
        <span className="relative flex w-full items-center">
          <span className={cn(line, "bg-weld/20")} />
          <span
            className={cn(
              "absolute left-0 rounded-full bg-weld transition-[width] duration-1000 ease-linear",
              z.line
            )}
            style={{ width: `${pct}%` }}
          />
          <span
            className={cn(
              "shuttle-glow absolute -translate-x-1/2 rounded-full bg-weld transition-[left] duration-1000 ease-linear",
              z.head
            )}
            style={{ left: `${pct}%` }}
          />
        </span>
      )
    }
    case "skipped":
      return <span className="h-px w-full bg-warp opacity-50" />
    case "pending":
      return <span className="h-px w-full bg-warp" />
  }
}

function groupsOf(steps: ThreadStep[]): string[] {
  return [...new Set(steps.map((s) => s.group))]
}

/** currentStep is the first step that isn't done, or the last one. */
export function currentStep(steps: ThreadStep[]): ThreadStep | undefined {
  return steps.find((s) => s.state !== "done") ?? steps.at(-1)
}

export function Thread({
  steps,
  size = "md",
  tooltips = false,
  draw = false,
  className,
}: {
  steps: ThreadStep[]
  size?: keyof typeof sizes
  tooltips?: boolean
  draw?: boolean
  className?: string
}) {
  const current = currentStep(steps)
  const at = current ? steps.indexOf(current) : 0
  const label = current
    ? `Step ${at + 1} of ${steps.length}, ${current.name}: ${(current.detail ?? stateLabel[current.state]).toLowerCase()}`
    : "No steps"
  return (
    <div
      role="img"
      aria-label={label}
      className={cn(
        "flex w-full items-center",
        size === "sm" ? "gap-1.5" : "gap-3",
        draw && "loom-draw",
        className
      )}
    >
      {groupsOf(steps).map((g) => {
        const inGroup = steps.filter((s) => s.group === g)
        return (
          <div
            key={g}
            className={cn(
              "flex items-center",
              size === "sm" ? "gap-px" : "gap-1"
            )}
            // The deploy phase can have dozens of steps; plan and approval
            // keep a readable width beside it.
            style={{ flexGrow: Math.max(inGroup.length, 2), flexBasis: 0 }}
          >
            {inGroup.map((s) =>
              tooltips ? (
                <Tooltip key={s.key}>
                  <TooltipTrigger
                    render={<span className="flex flex-1 items-center py-2" />}
                  >
                    <Cell s={s} size={size} />
                  </TooltipTrigger>
                  <TooltipContent>
                    <span className="font-medium">{s.name}</span>
                    <span className="ml-2 opacity-70">
                      {s.detail ?? stateLabel[s.state]}
                      {s.startedAt && s.finishedAt && (
                        <Fragment>
                          {" "}
                          in{" "}
                          {duration(
                            Date.parse(s.finishedAt) - Date.parse(s.startedAt)
                          )}
                        </Fragment>
                      )}
                    </span>
                  </TooltipContent>
                </Tooltip>
              ) : (
                <span key={s.key} className="flex flex-1 items-center py-1">
                  <Cell s={s} size={size} />
                </span>
              )
            )}
          </div>
        )
      })}
    </div>
  )
}

/** PhaseRuler names the phases under a thread, lined up with its groups. */
export function PhaseRuler({
  steps,
  active,
  className,
}: {
  steps: ThreadStep[]
  active?: string
  className?: string
}) {
  return (
    <div className={cn("flex gap-3 text-xs text-muted-foreground", className)}>
      {groupsOf(steps).map((g) => (
        <span
          key={g}
          style={{
            flexGrow: Math.max(steps.filter((s) => s.group === g).length, 2),
            flexBasis: 0,
          }}
          className={cn(
            "truncate border-l pl-2",
            active === g && "border-foreground/40 font-medium text-foreground"
          )}
        >
          {g}
        </span>
      ))}
    </div>
  )
}

/** RunDot is a run's state as a short thread end. */
export function RunDot({
  status,
  className,
}: {
  status: RunStatus
  className?: string
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-block h-1.5 w-3 shrink-0 rounded-full",
        status === "succeeded" && "bg-overdye",
        (status === "failed" || status === "blocked") && "bg-destructive",
        status === "canceled" && "bg-muted-foreground/50",
        status === "approved" && "bg-overdye/50",
        status === "running" && "thread-shuttle bg-weld",
        status === "waiting" && "bg-weld/20 ring-1 ring-weld ring-inset",
        className
      )}
    />
  )
}

export function RunBadge({ status }: { status: RunStatus }) {
  return (
    <Badge variant="outline" className="gap-1.5">
      <RunDot status={status} />
      {runStatusText[status]}
    </Badge>
  )
}

/** EnvBadge names an environment; production stands out a little. */
export function EnvBadge({
  env,
  className,
}: {
  env: string
  className?: string
}) {
  return (
    <Badge
      variant="outline"
      className={cn(
        "font-normal capitalize",
        env === "production"
          ? "border-foreground/25 text-foreground"
          : "text-muted-foreground",
        className
      )}
    >
      {env}
    </Badge>
  )
}

export function PersonAvatar({
  initials,
  name,
  className,
}: {
  initials: string
  name: string
  className?: string
}) {
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="inline-flex" />}>
        <Avatar className={cn("size-5", className)}>
          <AvatarFallback className="text-[9px] font-medium">
            {initials}
          </AvatarFallback>
        </Avatar>
      </TooltipTrigger>
      <TooltipContent>{name}</TooltipContent>
    </Tooltip>
  )
}

/** Code is an inline identifier: a SHA, an ID, a path. */
export function Code({
  children,
  className,
}: {
  children: ReactNode
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

/**
 * segmented makes a toggle group read as a segmented control: the chosen
 * segment in full ink, the rest dimmed, so the choice shows at a glance.
 */
export const segmented =
  "text-muted-foreground aria-pressed:bg-muted aria-pressed:text-foreground dark:aria-pressed:bg-input/50"

export type LogLevel = "info" | "warn" | "error"

export interface LogLine {
  id: string
  ts: number
  level: LogLevel
  source: string
  text: string
  /** The thread step the line belongs to, for filtering by step. */
  step?: string
}

const levels: LogLevel[] = ["info", "warn", "error"]

function highlight(text: string, q: string) {
  if (!q) return text
  const safe = q.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
  return text.split(new RegExp(`(${safe})`, "i")).map((p, i) =>
    i % 2 === 1 ? (
      <mark key={i} className="rounded-[2px] bg-weld/40 text-inherit">
        {p}
      </mark>
    ) : (
      <Fragment key={i}>{p}</Fragment>
    )
  )
}

/**
 * LogViewer is a terminal for a run's lines. It stays dark in both themes,
 * the way a terminal does, with the filters on the page's own surface.
 */
export function LogViewer({
  lines,
  live,
  name,
  step,
  stepName,
  onClearStep,
  empty,
}: {
  lines: LogLine[]
  live: boolean
  name: string
  step: string | null
  stepName?: string
  onClearStep: () => void
  /** What to say when there are no lines at all. */
  empty: string
}) {
  const [query, setQuery] = useState("")
  const [shown, setShown] = useState<string[]>(levels)
  const [follow, setFollow] = useState(true)
  const box = useRef<HTMLDivElement>(null)

  const visible = useMemo(
    () =>
      lines.filter(
        (l) =>
          shown.includes(l.level) &&
          (step === null || l.step === step) &&
          (!query ||
            `${l.source} ${l.text}`.toLowerCase().includes(query.toLowerCase()))
      ),
    [lines, shown, step, query]
  )

  // Follow the tail while it's on; scrolling up turns it off.
  useEffect(() => {
    const viewport = box.current?.querySelector<HTMLElement>(
      "[data-slot=scroll-area-viewport]"
    )
    if (follow && viewport) viewport.scrollTop = viewport.scrollHeight
  }, [visible.length, follow])

  const counts = useMemo(() => {
    const c: Record<LogLevel, number> = { info: 0, warn: 0, error: 0 }
    for (const l of lines) c[l.level]++
    return c
  }, [lines])

  const asText = visible
    .map(
      (l) =>
        `${new Date(l.ts).toISOString()} ${l.level.toUpperCase()} [${l.source}] ${l.text}`
    )
    .join("\n")

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="h-8 w-full rounded-full sm:w-56">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            data-page-search
            placeholder="Filter lines"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Filter log lines"
          />
          <InputGroupAddon align="inline-end">
            <Kbd>/</Kbd>
          </InputGroupAddon>
        </InputGroup>
        <ToggleGroup
          multiple
          variant="outline"
          size="sm"
          spacing={0}
          value={shown}
          onValueChange={(v) => setShown(v.length ? v : levels)}
          aria-label="Levels"
        >
          {levels.map((l) => (
            <ToggleGroupItem
              key={l}
              value={l}
              className={cn(segmented, "gap-1.5 capitalize")}
            >
              <span
                className={cn(
                  "size-1.5 rounded-full",
                  l === "info" && "bg-muted-foreground",
                  l === "warn" && "bg-weld",
                  l === "error" && "bg-destructive"
                )}
              />
              {l}
              <span className="text-muted-foreground tabular-nums">
                {counts[l]}
              </span>
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        {step !== null && (
          <Button
            variant="secondary"
            size="sm"
            className="rounded-full"
            onClick={onClearStep}
          >
            {stepName ?? step}
            <XIcon />
          </Button>
        )}
        <div className="ml-auto flex items-center gap-1">
          {live && (
            <Label className="mr-2 gap-2 text-xs font-normal text-muted-foreground">
              <Switch size="sm" checked={follow} onCheckedChange={setFollow} />
              Follow
            </Label>
          )}
          <CopyButton value={asText} label="Copy visible lines" />
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Download"
            className="text-muted-foreground"
            onClick={() => {
              const url = URL.createObjectURL(
                new Blob([asText], { type: "text/plain" })
              )
              const a = document.createElement("a")
              a.href = url
              a.download = `${name}.log`
              a.click()
              URL.revokeObjectURL(url)
            }}
          >
            <DownloadIcon />
          </Button>
        </div>
      </div>

      <div className="relative overflow-hidden rounded-xl bg-(--terminal) text-(--terminal-foreground) shadow-[inset_0_0_0_1px_var(--terminal-line)]">
        <div className="flex items-center gap-2 border-b border-(--terminal-line) px-4 py-2.5 font-mono text-[11px] text-(--terminal-muted)">
          <span className="truncate">{name}</span>
          <span className="ml-auto shrink-0 tabular-nums">
            {visible.length} of {lines.length} lines
          </span>
          {live && (
            <span className="flex shrink-0 items-center gap-1.5">
              <span className="size-1.5 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
              live
            </span>
          )}
        </div>
        <ScrollArea
          ref={box}
          onWheel={(e) => {
            if (e.deltaY < 0 && follow) setFollow(false)
          }}
          className="h-[520px]"
        >
          <div
            role="log"
            aria-live={live && follow ? "polite" : "off"}
            className="py-3 font-mono text-[12.5px] leading-[1.7]"
          >
            {visible.length === 0 ? (
              <p className="px-4 py-10 text-center font-sans text-sm text-(--terminal-muted)">
                {lines.length === 0
                  ? empty
                  : "No lines match. Clear the filter or pick another step."}
              </p>
            ) : (
              visible.map((l, i) => (
                <div
                  key={l.id}
                  className={cn(
                    "grid grid-cols-[2.25rem_4.75rem_6.5rem_1fr] gap-x-3 px-4 hover:bg-white/[0.04]",
                    l.level === "warn" &&
                      "bg-weld/[0.08] text-[color-mix(in_oklch,var(--weld)_75%,white)]",
                    l.level === "error" &&
                      "bg-destructive/[0.12] text-[color-mix(in_oklch,var(--destructive)_65%,white)]"
                  )}
                >
                  <span className="text-right text-(--terminal-muted)/60 tabular-nums select-none">
                    {i + 1}
                  </span>
                  <span className="text-(--terminal-muted) tabular-nums">
                    {clock(l.ts)}
                  </span>
                  <span className="truncate text-(--terminal-muted)">
                    {l.source}
                  </span>
                  <span className="break-words whitespace-pre-wrap">
                    {highlight(l.text, query)}
                  </span>
                </div>
              ))
            )}
          </div>
        </ScrollArea>
        {live && !follow && (
          <Button
            size="sm"
            variant="secondary"
            className="absolute right-4 bottom-4 rounded-full shadow-lg"
            onClick={() => setFollow(true)}
          >
            <ArrowDownIcon />
            Jump to latest
          </Button>
        )}
      </div>
    </div>
  )
}
