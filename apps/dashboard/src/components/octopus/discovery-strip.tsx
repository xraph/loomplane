// The strip above the domain cards: what FARP is watching and where its
// routes land, the source filter and Add route.
import { PlusIcon } from "lucide-react"
import { Fragment, type KeyboardEvent, type ReactNode } from "react"

import type { Board } from "@/client/forge/loomplane/types"
import { Panel } from "@/components/panel"
import { Button } from "@/components/ui/button"
import { isSet, relativeTime, useClock } from "@/lib/domains"
import {
  discoverySentence,
  strandedTail,
  type SourceFilter,
} from "@/lib/octopus"
import { cn } from "@/lib/utils"

const sources: { id: SourceFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "farp", label: "FARP" },
  { id: "here", label: "Added here" },
]

export function DiscoveryStrip({
  board,
  project,
  environment,
  source,
  onSource,
  onAdd,
}: {
  board: Board
  project: string
  environment: string
  source: SourceFilter
  onSource: (source: SourceFilter) => void
  /** Opens the add route dialog, or undefined when the viewer can't. */
  onAdd?: () => void
}) {
  const now = useClock()
  const lastChange = board.discovery.lastChange
  return (
    <Panel
      aria-label="Discovery"
      className="flex-row flex-wrap items-center gap-x-4 gap-y-2.5 bg-subtle py-3"
    >
      <span className="flex flex-[1_1_360px] items-center gap-2 text-[13px]">
        <span
          aria-hidden="true"
          className={cn(
            "size-2 flex-none rounded-full",
            board.live ? "bg-overdye" : "bg-weld"
          )}
        />
        {board.live ? (
          <Sentence board={board} project={project} environment={environment} />
        ) : (
          <span>{board.liveError}</span>
        )}
      </span>
      {board.live && isSet(lastChange) && (
        <span className="text-[12.5px] text-muted-foreground">
          Last change {relativeTime(lastChange, now)}
        </span>
      )}
      <SourceFilterGroup board={board} source={source} onSource={onSource} />
      {onAdd && (
        <Button type="button" onClick={onAdd}>
          <PlusIcon data-icon="inline-start" aria-hidden />
          Add route
        </Button>
      )}
    </Panel>
  )
}

/** HostNames sets hostnames in mono, as "a, b and c". */
export function HostNames({ hosts }: { hosts: string[] }) {
  return (
    <>
      {hosts.map((host, i) => (
        <Fragment key={host}>
          {i > 0 && (i === hosts.length - 1 ? " and " : ", ")}
          <span className="font-mono text-[12.5px]">{host}</span>
        </Fragment>
      ))}
    </>
  )
}

/**
 * StrandedFarp says FARP routes stay on picked hostnames that are all
 * detached, so they answer only on the target's private network.
 */
export function StrandedFarp({ hosts }: { hosts: string[] }) {
  return (
    <>
      FARP routes land on <HostNames hosts={hosts} />
      {strandedTail(hosts.length)}
    </>
  )
}

function Sentence({
  board,
  project,
  environment,
}: {
  board: Board
  project: string
  environment: string
}) {
  const { lead, hosts, tail } = discoverySentence(board, project, environment)
  return (
    <span>
      {boldScope(lead, `${project} / ${environment}`)}
      <HostNames hosts={hosts} />
      {tail}
    </span>
  )
}

// The project and environment FARP watches stand out in the sentence.
function boldScope(text: string, scope: string): ReactNode {
  const at = text.indexOf(scope)
  if (at < 0) return text
  return (
    <>
      {text.slice(0, at)}
      <b className="font-semibold">{scope}</b>
      {text.slice(at + scope.length)}
    </>
  )
}

function SourceFilterGroup({
  board,
  source,
  onSource,
}: {
  board: Board
  source: SourceFilter
  onSource: (source: SourceFilter) => void
}) {
  // Arrow keys move the choice, as in any radio group.
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step =
      event.key === "ArrowRight" || event.key === "ArrowDown"
        ? 1
        : event.key === "ArrowLeft" || event.key === "ArrowUp"
          ? -1
          : 0
    if (!step) return
    event.preventDefault()
    const at = sources.findIndex((s) => s.id === source)
    const next = sources[(at + step + sources.length) % sources.length]
    onSource(next.id)
    event.currentTarget
      .querySelector<HTMLButtonElement>(`[data-source="${next.id}"]`)
      ?.focus()
  }
  return (
    <div
      role="radiogroup"
      aria-label="Route source"
      onKeyDown={onKeyDown}
      className="flex max-w-full gap-0.5 overflow-x-auto rounded-lg bg-muted p-0.5"
    >
      {sources.map((s) => {
        const on = s.id === source
        return (
          <button
            key={s.id}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            data-source={s.id}
            onClick={() => onSource(s.id)}
            className={cn(
              "flex h-7 shrink-0 items-center gap-1.5 rounded-md px-2.5 text-xs font-medium whitespace-nowrap transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50",
              on
                ? "bg-background text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            {s.label}{" "}
            <span className="font-mono text-[11px] text-muted-foreground">
              {board.counts[s.id]}
            </span>
          </button>
        )
      })}
    </div>
  )
}
