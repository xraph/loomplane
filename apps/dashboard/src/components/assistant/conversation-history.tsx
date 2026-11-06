// Your conversations with the agent: searched, filtered by status and
// project, and grouped by project or by date, with the ones you pinned on
// top. A section folds away; pins and the grouping are this browser's.
import {
  CalendarBlankIcon,
  CaretRightIcon,
  DotsThreeIcon,
  PencilSimpleIcon,
  TrashIcon,
  FolderSimpleIcon,
  MagnifyingGlassIcon,
  PushPinIcon,
} from "@phosphor-icons/react"
import { useState } from "react"

import {
  useAgentConversationsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Conversation } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import { RunDot } from "@/components/runs"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  type GroupBy,
  type HistoryFilter,
  type StatusFilter,
  filterConversations,
  groupConversations,
  noFilter,
} from "@/lib/agent-dock"
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

export const statusText: Record<string, string> = {
  queued: "Queued",
  running: "Working",
  waiting: "Waiting on you",
  completed: "Answered",
  failed: "Failed",
  cancelled: "Stopped",
}

const statusFilters: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "waiting", label: "Needs you" },
  { value: "working", label: "Working" },
  { value: "failed", label: "Failed" },
]

export function ConversationHistory({
  selected,
  onSelect,
  groupBy,
  onGroupBy,
  pinned,
  onPin,
  onRename,
  onDelete,
  className,
}: {
  selected?: string
  /** onSelect opens a conversation. */
  onSelect: (c: Conversation) => void
  groupBy: GroupBy
  onGroupBy: (by: GroupBy) => void
  pinned: ReadonlySet<string>
  onPin: (id: string) => void
  className?: string
} & ConversationActions) {
  const list = useQuery(useAgentConversationsList)
  const projects = useQuery(useProjectsList)
  const now = useNow(60_000)
  const [filter, setFilter] = useState<HistoryFilter>(noFilter)
  const [folded, setFolded] = useState<ReadonlySet<string>>(() => new Set())
  const [deleting, setDeleting] = useState<Conversation>()

  const names = new Map(
    (projects.data?.projects ?? []).flatMap((p) => (p ? [[p.id, p.name]] : []))
  )
  const all = (list.data?.conversations ?? []).filter(
    (c): c is Conversation => !!c
  )
  const shown = filterConversations(all, filter, names)
  const groups = groupConversations(shown, groupBy, names, pinned, now)
  const filtered =
    filter.query !== "" || filter.status !== "all" || filter.project !== "all"
  // Only projects someone has talked about are worth filtering by.
  const talkedAbout = [
    ...new Set(all.flatMap((c) => (c.projectId ? [c.projectId] : []))),
  ]
  const fold = (key: string) =>
    setFolded((cur) => {
      const next = new Set(cur)
      if (!next.delete(key)) next.add(key)
      return next
    })

  return (
    <nav
      aria-label="Conversations"
      className={cn("flex min-h-0 flex-col", className)}
    >
      <div className="grid grid-cols-[minmax(0,1fr)] gap-2 border-b p-2.5">
        <div className="flex items-center gap-1.5">
          <InputGroup className="h-8 min-w-0 flex-1">
            <InputGroupAddon>
              <MagnifyingGlassIcon />
            </InputGroupAddon>
            <InputGroupInput
              aria-label="Search conversations"
              placeholder="Search conversations"
              value={filter.query}
              onChange={(e) =>
                setFilter((f) => ({ ...f, query: e.target.value }))
              }
            />
          </InputGroup>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={
              groupBy === "project" ? "Group by date" : "Group by project"
            }
            title={groupBy === "project" ? "Group by date" : "Group by project"}
            onClick={() =>
              onGroupBy(groupBy === "project" ? "date" : "project")
            }
          >
            {groupBy === "project" ? (
              <FolderSimpleIcon />
            ) : (
              <CalendarBlankIcon />
            )}
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
          <div role="radiogroup" aria-label="Status" className="flex gap-1">
            {statusFilters.map((s) => (
              <button
                key={s.value}
                type="button"
                role="radio"
                aria-checked={filter.status === s.value}
                onClick={() => setFilter((f) => ({ ...f, status: s.value }))}
                className={cn(
                  "h-6 shrink-0 rounded-full px-2.5 text-[12px] transition-colors",
                  filter.status === s.value
                    ? "bg-foreground text-background"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground"
                )}
              >
                {s.label}
              </button>
            ))}
          </div>
          {talkedAbout.length > 0 && (
            <Select
              value={filter.project}
              onValueChange={(v) =>
                setFilter((f) => ({ ...f, project: v ?? "all" }))
              }
            >
              <SelectTrigger
                size="sm"
                aria-label="Project"
                className="ml-auto h-6 max-w-full min-w-0 rounded-full border-transparent px-2 text-[12px] hover:bg-muted"
              >
                <SelectValue>
                  {(v: string) =>
                    v === "all"
                      ? "All projects"
                      : v === "workspace"
                        ? "Whole workspace"
                        : (names.get(v) ?? "A deleted project")
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectContent align="end">
                <SelectItem value="all">All projects</SelectItem>
                <SelectItem value="workspace">Whole workspace</SelectItem>
                <SelectSeparator />
                {talkedAbout.map((id) => (
                  <SelectItem key={id} value={id}>
                    {names.get(id) ?? "A deleted project"}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-1.5">
        {list.status === "error" ? (
          <div className="p-2">
            <QueryError
              error={list.error}
              fallback="Your conversations couldn't be loaded."
              retry={list.refetch}
            />
          </div>
        ) : !list.data ? (
          <div className="grid gap-1.5 p-1">
            <Skeleton className="h-11 w-full rounded-lg" />
            <Skeleton className="h-11 w-full rounded-lg" />
            <Skeleton className="h-11 w-full rounded-lg" />
          </div>
        ) : all.length === 0 ? (
          <p className="px-2.5 py-6 text-center text-[12.5px] text-muted-foreground">
            Your conversations with the agent show up here.
          </p>
        ) : groups.length === 0 ? (
          <div className="grid justify-items-center gap-2 px-2.5 py-6 text-center text-[12.5px] text-muted-foreground">
            No conversations match.
            {filtered && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setFilter(noFilter)}
              >
                Clear filters
              </Button>
            )}
          </div>
        ) : (
          groups.map((g) => {
            const open = !folded.has(g.key)
            return (
              <section key={g.key} aria-label={g.label} className="mb-1">
                <button
                  type="button"
                  aria-expanded={open}
                  onClick={() => fold(g.key)}
                  className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-[11.5px] font-medium text-muted-foreground hover:text-foreground"
                >
                  <CaretRightIcon
                    className={cn(
                      "size-3 transition-transform",
                      open && "rotate-90"
                    )}
                  />
                  {g.key === "pinned" && <PushPinIcon className="size-3" />}
                  <span className="truncate">{g.label}</span>
                  <span className="ml-auto font-mono tabular-nums">
                    {g.conversations.length}
                  </span>
                </button>
                {open && (
                  <ul className="grid gap-px">
                    {g.conversations.map((c) => (
                      <Row
                        key={c.id}
                        conversation={c}
                        active={c.id === selected}
                        pinned={pinned.has(c.id)}
                        project={
                          groupBy === "date" || g.key === "pinned"
                            ? c.projectId
                              ? (names.get(c.projectId) ?? "A deleted project")
                              : undefined
                            : undefined
                        }
                        now={now}
                        onSelect={() => onSelect(c)}
                        onPin={() => onPin(c.id)}
                        onRename={
                          onRename ? (title) => onRename(c, title) : undefined
                        }
                        onAskDelete={
                          onDelete ? () => setDeleting(c) : undefined
                        }
                      />
                    ))}
                  </ul>
                )}
              </section>
            )
          })
        )}
      </div>
      {onDelete && (
        <DeleteDialog
          conversation={deleting}
          onClose={() => setDeleting(undefined)}
          onDelete={onDelete}
        />
      )}
    </nav>
  )
}

/** What a row's menu does to a conversation. Pinning goes through onPin;
 * rename and delete only show when the history can do them. */
export type ConversationActions = {
  onRename?: (c: Conversation, title: string) => Promise<void>
  onDelete?: (c: Conversation) => Promise<void>
}

function Row({
  conversation: c,
  active,
  pinned,
  project,
  now,
  onSelect,
  onPin,
  onRename,
  onAskDelete,
}: {
  conversation: Conversation
  active: boolean
  pinned: boolean
  /** The project's name, when the section doesn't already say it. */
  project?: string
  now: number
  onSelect: () => void
  onPin: () => void
  onRename?: (title: string) => Promise<void>
  onAskDelete?: () => void
}) {
  const title = c.title || "Untitled"
  const [renaming, setRenaming] = useState(false)
  const [draft, setDraft] = useState(title)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()

  const save = async () => {
    const next = draft.trim()
    if (!next || next === title || !onRename) {
      setRenaming(false)
      return
    }
    setSaving(true)
    setError(undefined)
    try {
      await onRename(next)
      setRenaming(false)
    } catch (err) {
      setError(messageOf(err, "It couldn't be renamed."))
    } finally {
      setSaving(false)
    }
  }

  if (renaming)
    return (
      <li className="grid gap-1 rounded-lg bg-muted px-2 py-1.5">
        <Input
          autoFocus
          aria-label="Conversation name"
          value={draft}
          maxLength={120}
          disabled={saving}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              void save()
            } else if (e.key === "Escape") {
              e.preventDefault()
              setRenaming(false)
            }
          }}
          onBlur={() => void save()}
          className="h-7 bg-background text-[13px]"
        />
        {error && (
          <p role="alert" className="text-[11.5px] text-destructive">
            {error}
          </p>
        )}
      </li>
    )

  return (
    <li className="group/row relative">
      <button
        type="button"
        aria-current={active ? "page" : undefined}
        onClick={onSelect}
        className={cn(
          "grid w-full gap-0.5 rounded-lg py-1.5 pr-14 pl-2.5 text-left transition-colors",
          active
            ? "bg-muted text-foreground"
            : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
        )}
      >
        <span className="flex min-w-0 items-center gap-2">
          <RunDot status={dotStatus(c.status)} />
          <span className="truncate text-[13px] font-medium text-foreground">
            {title}
          </span>
        </span>
        <span className="flex min-w-0 items-center gap-1.5 pl-5 text-[11.5px]">
          {project && (
            <span className="truncate rounded bg-subtle px-1.5 py-px text-foreground ring-1 ring-border">
              {project}
            </span>
          )}
          <span className="shrink-0">{statusText[c.status] ?? c.status}</span>
          <span aria-hidden className="shrink-0">
            ·
          </span>
          <span className="shrink-0">{ago(c.updatedAt, now)}</span>
        </span>
      </button>
      <div className="absolute top-1.5 right-1.5 flex items-center">
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label={pinned ? `Unpin ${title}` : `Pin ${title}`}
          title={pinned ? "Unpin" : "Pin"}
          onClick={onPin}
          className={cn(
            "text-muted-foreground",
            !pinned &&
              "opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100"
          )}
        >
          <PushPinIcon weight={pinned ? "fill" : "regular"} />
        </Button>
        {(onRename || onAskDelete) && (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label={`More for ${title}`}
                  className="text-muted-foreground opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100 data-popup-open:opacity-100"
                />
              }
            >
              <DotsThreeIcon weight="bold" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-40">
              <DropdownMenuItem onClick={onPin}>
                <PushPinIcon />
                {pinned ? "Unpin" : "Pin"}
              </DropdownMenuItem>
              {onRename && (
                <DropdownMenuItem
                  onClick={() => {
                    setDraft(title)
                    setError(undefined)
                    setRenaming(true)
                  }}
                >
                  <PencilSimpleIcon />
                  Rename
                </DropdownMenuItem>
              )}
              {onAskDelete && (
                <DropdownMenuItem variant="destructive" onClick={onAskDelete}>
                  <TrashIcon />
                  Delete
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
    </li>
  )
}

/** DeleteDialog asks before a conversation goes. */
function DeleteDialog({
  conversation,
  onClose,
  onDelete,
}: {
  conversation?: Conversation
  onClose: () => void
  onDelete: (c: Conversation) => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const run = async () => {
    if (!conversation) return
    setBusy(true)
    setError(undefined)
    try {
      await onDelete(conversation)
      onClose()
    } catch (err) {
      setError(messageOf(err, "It couldn't be deleted."))
    } finally {
      setBusy(false)
    }
  }
  return (
    <AlertDialog
      open={!!conversation}
      onOpenChange={(open) => {
        if (!open) {
          setError(undefined)
          onClose()
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete this conversation?</AlertDialogTitle>
          <AlertDialogDescription>
            &ldquo;{conversation?.title || "Untitled"}&rdquo; leaves your list
            for good. If the agent is still working on it, it stops.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-[12.5px] text-destructive">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={busy}
            onClick={() => void run()}
          >
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
