// The person's conversations with the agent, the most recently updated
// first: a column on wide screens and a select on narrow ones.
import { PlusIcon } from "lucide-react"

import {
  useAgentConversationsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Conversation } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { RunDot } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { ago } from "@/lib/relative-time"
import type { RunStatus } from "@/lib/runs"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** dotStatus is a conversation's latest turn in the runs' colors. */
export function dotStatus(status: string): RunStatus {
  switch (status) {
    case "completed":
      return "succeeded"
    case "failed":
      return "failed"
    case "cancelled":
      return "canceled"
    case "waiting":
      return "waiting"
    default:
      return "running"
  }
}

const statusText: Record<string, string> = {
  queued: "Queued",
  running: "Working",
  waiting: "Waiting on you",
  completed: "Answered",
  failed: "Failed",
  cancelled: "Stopped",
}

const NEW = "new"

export function ConversationList({
  selected,
  onSelect,
}: {
  selected?: string
  /** onSelect opens a conversation, or a new one with undefined. */
  onSelect: (id: string | undefined) => void
}) {
  const list = useQuery(useAgentConversationsList)
  const projects = useQuery(useProjectsList)
  const now = useNow(60_000)
  const names = new Map(
    (projects.data?.projects ?? []).map((p) => [p.id, p.name])
  )
  const conversations = (list.data?.conversations ?? []).filter(
    (c): c is Conversation => !!c
  )
  const titleOf = (id: string) =>
    conversations.find((c) => c.id === id)?.title ?? "A conversation"

  return (
    <nav aria-label="Conversations" className="grid min-h-0 gap-2">
      {/* Narrow screens: a select. */}
      <div className="flex gap-2 md:hidden">
        <Select
          value={selected ?? NEW}
          onValueChange={(v) => onSelect(!v || v === NEW ? undefined : v)}
        >
          <SelectTrigger
            aria-label="Conversation"
            className="w-full min-w-0 flex-1"
          >
            <SelectValue>
              {(v: string) => (v === NEW ? "New conversation" : titleOf(v))}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NEW}>New conversation</SelectItem>
            {conversations.map((c) => (
              <SelectItem key={c.id} value={c.id}>
                {c.title || "Untitled"}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* Wide screens: the list. */}
      <div className="hidden min-h-0 flex-col gap-1 md:flex">
        <Button
          variant={selected ? "outline" : "secondary"}
          className="mb-1 justify-start"
          onClick={() => onSelect(undefined)}
        >
          <PlusIcon />
          New conversation
        </Button>
        {list.status === "error" ? (
          <QueryError
            error={list.error}
            fallback="Your conversations couldn't be loaded."
            retry={list.refetch}
          />
        ) : !list.data ? (
          <div className="grid gap-1.5">
            <Skeleton className="h-12 w-full rounded-lg" />
            <Skeleton className="h-12 w-full rounded-lg" />
          </div>
        ) : conversations.length === 0 ? (
          <p className="px-2 py-3 text-[12.5px] text-muted-foreground">
            Your conversations show up here.
          </p>
        ) : (
          <ul className="grid min-h-0 gap-0.5 overflow-y-auto">
            {conversations.map((c) => {
              const project = c.projectId ? names.get(c.projectId) : undefined
              const active = c.id === selected
              return (
                <li key={c.id}>
                  <button
                    type="button"
                    aria-current={active ? "page" : undefined}
                    onClick={() => onSelect(c.id)}
                    className={cn(
                      "grid w-full gap-1 rounded-lg px-2.5 py-2 text-left transition-colors",
                      active
                        ? "bg-muted text-foreground"
                        : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                    )}
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <RunDot status={dotStatus(c.status)} />
                      <span className="truncate text-[13px] font-medium text-foreground">
                        {c.title || "Untitled"}
                      </span>
                    </span>
                    <span className="flex min-w-0 items-center gap-1.5 pl-5 text-[11.5px]">
                      {project && (
                        <span className="truncate rounded bg-subtle px-1.5 py-px text-foreground">
                          {project}
                        </span>
                      )}
                      <span className="shrink-0">
                        {statusText[c.status] ?? c.status}
                      </span>
                      <span aria-hidden className="shrink-0">
                        ·
                      </span>
                      <span className="shrink-0">{ago(c.updatedAt, now)}</span>
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </nav>
  )
}
