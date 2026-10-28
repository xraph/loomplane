// The pieces every deployment view draws a run with. The thread is the
// run's steps as a row of short bars: overdye green when done, flight blue
// while running, weld while waiting on someone, red when it failed, and
// warp for what's left. The status tile and pill say the same in words.
import {
  ArrowDownIcon,
  BanIcon,
  CheckIcon,
  CopyIcon,
  DownloadIcon,
  HourglassIcon,
  LoaderCircleIcon,
  PauseIcon,
  SearchIcon,
  TriangleAlertIcon,
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
  type Run,
  type RunStatus,
  runStatusText,
  type ThreadState,
  type ThreadStep,
} from "@/lib/runs"
import { cn } from "@/lib/utils"

export const stateLabel: Record<ThreadState, string> = {
  done: "Done",
  running: "Running",
  waiting: "Waiting for approval",
  failed: "Failed",
  canceled: "Canceled",
  pending: "Not started",
  skipped: "Skipped",
}

const sizes = {
  sm: { line: "h-[5px]" },
  md: { line: "h-[5px]" },
  lg: { line: "h-1.5" },
} as const

// Each step is a short bar: overdye when done, flight blue while it runs,
// weld while it waits on someone, failed red, and warp until it starts.
function Cell({ s, size }: { s: ThreadStep; size: keyof typeof sizes }) {
  const line = cn("w-full rounded-full", sizes[size].line)
  switch (s.state) {
    case "done":
      return <span className={cn(line, "bg-overdye")} />
    case "failed":
      return <span className={cn(line, "bg-failed")} />
    case "canceled":
      return <span className={cn(line, "bg-muted-foreground/40")} />
    case "waiting":
      return (
        <span
          className={cn(
            line,
            "animate-pulse bg-weld motion-reduce:animate-none"
          )}
        />
      )
    case "running": {
      const pct = Math.max((s.progress ?? 0) * 100, 8)
      return (
        <span className={cn("relative flex w-full items-center")}>
          <span className={cn(line, "bg-flight/25")} />
          <span
            className={cn(
              "thread-shuttle absolute left-0 rounded-full bg-flight transition-[width] duration-1000 ease-linear",
              sizes[size].line
            )}
            style={{ width: `${pct}%` }}
          />
        </span>
      )
    }
    case "skipped":
      return <span className={cn(line, "bg-warp opacity-50")} />
    case "pending":
      return <span className={cn(line, "bg-warp")} />
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
        size === "sm" ? "gap-1" : "gap-1.5",
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
              size === "sm" ? "gap-0.5" : "gap-1"
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
        (status === "waiting" || status === "paused") &&
          "bg-weld/20 ring-1 ring-weld ring-inset",
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

/** The fixed status colors: tone is what a run's state is drawn in. */
export type Tone = "live" | "waiting" | "flight" | "failed" | "quiet"

export function toneOf(status: RunStatus): Tone {
  switch (status) {
    case "succeeded":
      return "live"
    case "waiting":
    case "paused":
      return "waiting"
    case "running":
    case "approved":
      return "flight"
    case "failed":
    case "blocked":
      return "failed"
    default:
      return "quiet"
  }
}

export const toneClass: Record<
  Tone,
  { ink: string; tint: string; dot: string }
> = {
  live: { ink: "text-overdye-ink", tint: "bg-overdye-tint", dot: "bg-overdye" },
  waiting: { ink: "text-weld-ink", tint: "bg-weld-tint", dot: "bg-weld" },
  flight: { ink: "text-flight-ink", tint: "bg-flight-tint", dot: "bg-flight" },
  failed: { ink: "text-destructive", tint: "bg-failed-tint", dot: "bg-failed" },
  quiet: {
    ink: "text-muted-foreground",
    tint: "bg-muted",
    dot: "bg-muted-foreground/50",
  },
}

const statusIcons: Record<RunStatus, typeof CheckIcon> = {
  succeeded: CheckIcon,
  waiting: HourglassIcon,
  paused: PauseIcon,
  running: LoaderCircleIcon,
  approved: LoaderCircleIcon,
  failed: XIcon,
  blocked: TriangleAlertIcon,
  canceled: BanIcon,
}

/** StatusTile is a run's state as an icon on its tint, for list rows. */
export function StatusTile({
  status,
  className,
}: {
  status: RunStatus
  className?: string
}) {
  const t = toneClass[toneOf(status)]
  const Icon = statusIcons[status]
  return (
    <span
      className={cn(
        "inline-flex size-7 shrink-0 items-center justify-center rounded-lg",
        t.tint,
        t.ink,
        className
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn(
          "size-3.5",
          status === "running" && "animate-spin motion-reduce:animate-none"
        )}
        strokeWidth={2.4}
      />
      <span className="sr-only">{runStatusText[status]}</span>
    </span>
  )
}

/** StatusPill names a state in its tone, with a dot so it isn't color alone. */
export function StatusPill({
  tone,
  children,
  className,
}: {
  tone: Tone
  children: ReactNode
  className?: string
}) {
  const t = toneClass[tone]
  return (
    <span
      className={cn(
        "inline-flex h-5 w-fit shrink-0 items-center gap-1.5 rounded-full px-2 text-xs font-medium whitespace-nowrap",
        t.tint,
        t.ink,
        className
      )}
    >
      <span className={cn("size-1.5 rounded-full", t.dot)} aria-hidden="true" />
      {children}
    </span>
  )
}

/** ChangeChips are what a run's plan does, as short counts. */
export function ChangeChips({ run: r }: { run: Run }) {
  const p = r.plan
  const chip =
    "inline-flex h-5 items-center rounded-md px-1.5 font-mono text-xs"
  if (p.problems.length > 0) {
    return (
      <span className={cn(chip, "bg-failed-tint text-destructive")}>
        {p.problems.length} {p.problems.length === 1 ? "problem" : "problems"}
      </span>
    )
  }
  const s = p.summary
  const parts = [
    s.create > 0 && (
      <span key="c" className={cn(chip, "bg-flight-tint text-flight-ink")}>
        +{s.create} create
      </span>
    ),
    s.update > 0 && (
      <span key="u" className={cn(chip, "bg-muted text-foreground")}>
        ~{s.update} update
      </span>
    ),
    s.replace > 0 && (
      <span key="r" className={cn(chip, "bg-weld-tint text-weld-ink")}>
        {s.replace} replace
      </span>
    ),
    s.delete > 0 && (
      <span key="d" className={cn(chip, "bg-failed-tint text-destructive")}>
        −{s.delete} delete
      </span>
    ),
  ].filter(Boolean)
  if (parts.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {p.status === "planning" ? "Planning" : "No changes"}
      </span>
    )
  }
  return <span className="inline-flex flex-wrap gap-1">{parts}</span>
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
        "rounded-md px-1.5 font-medium capitalize",
        env === "production"
          ? "border-border text-foreground"
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
        <InputGroup className="h-8 w-full sm:w-56">
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
              <p className="px-4 py-10 text-center font-sans text-[13px] text-(--terminal-muted)">
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
            className="absolute right-4 bottom-4 shadow-lg"
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
