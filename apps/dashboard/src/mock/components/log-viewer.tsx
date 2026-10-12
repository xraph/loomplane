// A terminal for a deployment's logs. It stays dark in both themes, the way
// a terminal does, with the filters on the page's own surface above it.
import { ArrowDownIcon, DownloadIcon, SearchIcon, XIcon } from "lucide-react"
import { Fragment, useEffect, useMemo, useRef, useState } from "react"

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
import { cn } from "@/lib/utils"
import { CopyButton, segmented } from "@/mock/components/bits"
import { steps } from "@/mock/data"
import { clock } from "@/mock/format"
import type { LogLevel, LogLine } from "@/mock/logs"

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

export function LogViewer({
  lines,
  live,
  name,
  step,
  onClearStep,
}: {
  lines: LogLine[]
  live: boolean
  name: string
  step: number | null
  onClearStep: () => void
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
            {steps[step]?.name}
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
          <span>
            {name}
            {step !== null && ` / ${steps[step]?.key}`}
          </span>
          <span className="ml-auto tabular-nums">
            {visible.length} of {lines.length} lines
          </span>
          {live && (
            <span className="flex items-center gap-1.5">
              <span className="size-1.5 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
              streaming
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
                No lines match. Clear the filter or pick another step.
              </p>
            ) : (
              visible.map((l, i) => (
                <div
                  key={l.id}
                  className={cn(
                    "grid grid-cols-[2.25rem_4.75rem_5.5rem_1fr] gap-x-3 px-4 hover:bg-white/[0.04]",
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
            {live && follow && (
              <div className="px-4 pt-1">
                <span className="ml-[calc(2.25rem+0.75rem)] inline-block h-4 w-2 translate-y-0.5 animate-pulse bg-(--terminal-foreground)/70 motion-reduce:animate-none" />
              </div>
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
