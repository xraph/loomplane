// The agent's panel, part of the layout rather than a page of its own. It
// docks on the right and pushes the page over (drag its edge to resize),
// floats over the page in the corner, or takes the page's whole width with
// your conversations beside it. On a phone it covers the screen. Where it
// sits, its width and whether it's open survive a reload (lib/agent-dock).
//
// It mounts the first time it opens and stays mounted when closed, so an
// answer the agent is still writing keeps streaming while it's hidden.
import {
  ArrowsInIcon,
  ArrowsOutIcon,
  CheckIcon,
  ClockCounterClockwiseIcon,
  FolderSimpleIcon,
  GlobeIcon,
  MapPinIcon,
  NotePencilIcon,
  PictureInPictureIcon,
  PlusIcon,
  SidebarSimpleIcon,
  XIcon,
} from "@phosphor-icons/react"
import { useEffect, useRef, useState } from "react"

import {
  useAgentConversationsDelete,
  useAgentConversationsList,
  useAgentConversationsUpdate,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Conversation } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { AgentOrb } from "@/components/assistant/agent-orb"
import { ConversationHistory } from "@/components/assistant/conversation-history"
import { ConversationView } from "@/components/assistant/conversation-view"
import { type AgentDock as Dock, useAgentDock } from "@/components/shell"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useIsMobile } from "@/hooks/use-mobile"
import { DOCK_DEFAULT, type DockMode, clampWidth } from "@/lib/agent-dock"
import { type PageContext, pageContextOf } from "@/lib/page-context"
import { usePath, useSearchParams } from "@/lib/router"
import { cn } from "@/lib/utils"

type Placement = DockMode | "mobile"

const modes: { mode: DockMode; label: string; icon: React.ReactNode }[] = [
  {
    mode: "docked",
    label: "Dock to the right",
    icon: <SidebarSimpleIcon className="-scale-x-100" />,
  },
  {
    mode: "floating",
    label: "Float over the page",
    icon: <PictureInPictureIcon />,
  },
  { mode: "focus", label: "Full width", icon: <ArrowsOutIcon /> },
]

/** suggestionsFor is what a new conversation offers to ask: about the
 * page you're on when it's a thing, else the project, else the whole
 * workspace. */
export function suggestionsFor(project?: string, page?: PageContext): string[] {
  const env = page?.environment
  switch (page?.kind) {
    case "service":
      return [
        `How is ${page.service} doing${env ? ` in ${env}` : ""}?`,
        `What changed in ${page.service} recently?`,
        `Why did ${page.service}'s last deployment fail?`,
        `What does ${page.service} cost to run?`,
      ]
    case "deployment":
      return [
        "What does this deployment change?",
        "Why did this deployment fail?",
        "Is this deployment safe to promote?",
      ]
    case "environment":
      return [
        `What's running in ${env}?`,
        `What's ${env} behind on?`,
        `Is anything failing in ${env}?`,
      ]
    case "fleet":
      return ["How healthy is this fleet?", "Which instances are behind?"]
    case "cloud":
      return ["What runs on this cloud?", "What does this cloud cost?"]
  }
  if (project)
    return [
      `How is ${project} doing right now?`,
      `What's the latest commit and release of ${project}?`,
      `Why did ${project}'s last deployment fail?`,
      `What does ${project} cost to run each month?`,
    ]
  return [
    "What's deploying right now?",
    "What's waiting on me?",
    "Which deployments failed this week, and why?",
    "Which services cost the most to run?",
  ]
}

export function AgentDock() {
  const dock = useAgentDock()
  const mobile = useIsMobile()
  // Mounted once opened, then only hidden.
  const [seen, setSeen] = useState(false)
  if (dock?.open && !seen) setSeen(true)
  const [drag, setDrag] = useState<number>()
  const aside = useRef<HTMLElement>(null)
  const open = !!dock?.open

  // Opening puts you in the box. (Closing gives the keys back to the page
  // before the panel hides: the shell's releaseFocus.)
  useEffect(() => {
    if (open)
      aside.current
        ?.querySelector<HTMLTextAreaElement>("textarea:not(:disabled)")
        ?.focus({ preventScroll: true })
  }, [open, seen])

  if (!dock || !seen) return null

  const placement: Placement = mobile ? "mobile" : dock.mode
  const width = drag ?? clampWidth(dock.width)

  return (
    <aside
      ref={aside}
      aria-label="Agent"
      data-agent-dock=""
      hidden={!dock.open}
      data-placement={placement}
      style={placement === "docked" ? { width } : undefined}
      className={cn(
        "flex flex-col bg-background",
        placement === "docked" && "sticky top-0 h-svh shrink-0 border-l",
        placement === "focus" && "sticky top-0 h-svh min-w-0 flex-1",
        placement === "floating" &&
          "fixed right-4 bottom-11 z-40 h-[min(720px,calc(100svh-6rem))] w-[min(440px,calc(100vw-2rem))] overflow-hidden rounded-2xl border shadow-2xl shadow-black/10",
        placement === "mobile" && "fixed inset-0 z-50"
      )}
    >
      {placement === "docked" && (
        <ResizeHandle
          width={width}
          onDrag={setDrag}
          onDone={(w) => {
            setDrag(undefined)
            dock.setWidth(w)
          }}
        />
      )}
      <AgentPanel dock={dock} placement={placement} />
    </aside>
  )
}

/** ResizeHandle is the docked panel's left edge: drag it, or focus it and
 * use the arrow keys. A double click goes back to the default width. */
function ResizeHandle({
  width,
  onDrag,
  onDone,
}: {
  width: number
  onDrag: (width: number) => void
  onDone: (width: number) => void
}) {
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize the agent's panel"
      aria-valuenow={width}
      tabIndex={0}
      className="group/handle absolute inset-y-0 left-0 z-10 w-2 -translate-x-1/2 cursor-col-resize touch-none outline-none"
      onPointerDown={(e) => {
        e.preventDefault()
        const startX = e.clientX
        const startW = width
        let last = startW
        const el = e.currentTarget
        el.setPointerCapture(e.pointerId)
        const move = (ev: PointerEvent) => {
          last = clampWidth(startW + (startX - ev.clientX))
          onDrag(last)
        }
        const up = () => {
          el.removeEventListener("pointermove", move)
          el.removeEventListener("pointerup", up)
          el.removeEventListener("pointercancel", up)
          onDone(last)
        }
        el.addEventListener("pointermove", move)
        el.addEventListener("pointerup", up)
        el.addEventListener("pointercancel", up)
      }}
      onDoubleClick={() => onDone(DOCK_DEFAULT)}
      onKeyDown={(e) => {
        const step = e.shiftKey ? 64 : 16
        if (e.key === "ArrowLeft") onDone(clampWidth(width + step))
        else if (e.key === "ArrowRight") onDone(clampWidth(width - step))
        else return
        e.preventDefault()
      }}
    >
      <span className="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-transparent transition-colors group-hover/handle:bg-primary/60 group-focus-visible/handle:bg-primary" />
    </div>
  )
}

function AgentPanel({ dock, placement }: { dock: Dock; placement: Placement }) {
  const list = useQuery(useAgentConversationsList)
  const projects = useQuery(useProjectsList)
  const names = new Map(
    (projects.data?.projects ?? []).flatMap((p) => (p ? [[p.id, p.name]] : []))
  )
  const open = dock.conversationId
    ? list.data?.conversations.find((c) => c?.id === dock.conversationId)
    : undefined
  // Pins, names and deletions are the server's, so they follow you to
  // every browser.
  const update = useMutation(useAgentConversationsUpdate)
  const remove = useMutation(useAgentConversationsDelete)
  const invalidate = useInvalidate()
  const pinned = new Set(
    (list.data?.conversations ?? []).flatMap((c) => (c?.pinned ? [c.id] : []))
  )
  const history = {
    pinned,
    onPin: (id: string) =>
      void update
        .mutateAsync({ path: { id }, body: { pinned: !pinned.has(id) } })
        .then(() => invalidate(useAgentConversationsList))
        .catch(() => undefined),
    onRename: async (c: Conversation, title: string) => {
      await update.mutateAsync({ path: { id: c.id }, body: { title } })
      invalidate(useAgentConversationsList)
    },
    onDelete: async (c: Conversation) => {
      await remove.mutateAsync({ path: { id: c.id } })
      if (c.id === dock.conversationId) dock.newConversation()
      invalidate(useAgentConversationsList)
    },
    groupBy: dock.groupBy,
    onGroupBy: dock.setGroupBy,
  }
  // A conversation's own record says what it's about; a new one is about
  // the scope the panel holds.
  const projectId = open ? open.projectId || undefined : dock.projectId
  const projectName = projectId
    ? (names.get(projectId) ?? "this project")
    : undefined

  // What you're looking at, offered with the next message when it fits
  // what the conversation is about: a page in another project doesn't.
  const path = usePath()
  const search = useSearchParams()
  const page = pageContextOf(path, search, names)
  const sendable =
    page && (!page.projectId || !projectId || page.projectId === projectId)
      ? page
      : undefined
  // A page you took off stays off until you move to another one.
  const [dropped, setDropped] = useState<string>()
  const sending = sendable && sendable.path !== dropped ? sendable : undefined

  const wide = placement === "focus"
  // Beside the conversation on a wide panel, over it on a narrow one.
  const [historyOpen, setHistoryOpen] = useState<boolean>()
  const showHistory = historyOpen ?? wide
  const besideHistory = wide && showHistory
  const overHistory = !wide && showHistory

  const title = dock.conversationId
    ? open?.title || "Untitled"
    : "New conversation"

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-[52px] shrink-0 items-center gap-1 border-b px-2">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={showHistory ? "Hide conversations" : "Show conversations"}
          aria-pressed={showHistory}
          title="Conversations"
          onClick={() => setHistoryOpen(!showHistory)}
          className={cn(showHistory && "bg-muted")}
        >
          <ClockCounterClockwiseIcon />
        </Button>
        <div className="flex min-w-0 flex-1 items-center gap-2 px-1">
          <AgentOrb size={20} state={dock.activity} />
          <h2 className="truncate text-[13.5px] font-semibold">{title}</h2>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="New conversation"
          title="New conversation"
          disabled={!dock.conversationId}
          onClick={() => {
            dock.newConversation()
            if (!wide) setHistoryOpen(false)
          }}
        >
          <NotePencilIcon />
        </Button>
        {placement !== "mobile" && (
          <>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={wide ? "Leave full width" : "Full width"}
              title={wide ? "Leave full width" : "Full width"}
              className="hidden lg:inline-flex"
              onClick={() => dock.setMode(wide ? "docked" : "focus")}
            >
              {wide ? <ArrowsInIcon /> : <ArrowsOutIcon />}
            </Button>
            <PlacementMenu mode={dock.mode} onMode={dock.setMode} />
          </>
        )}
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Close the agent"
          title="Close (⌘J)"
          onClick={() => dock.setOpen(false)}
        >
          <XIcon />
        </Button>
      </header>

      <div className="relative flex min-h-0 flex-1">
        {besideHistory && (
          <ConversationHistory
            className="w-[300px] shrink-0 border-r"
            selected={dock.conversationId}
            onSelect={(c) => dock.select(c.id, c.projectId || undefined)}
            {...history}
          />
        )}
        <ConversationView
          className="min-h-0 min-w-0"
          compact={placement !== "focus"}
          wide={wide}
          conversationId={dock.conversationId}
          projectId={dock.conversationId ? undefined : dock.projectId}
          onConversation={dock.onConversation}
          onActivity={dock.setActivity}
          suggestions={suggestionsFor(projectName, sendable)}
          context={sending}
          scope={
            <>
              {dock.conversationId ? (
                <ScopeChip
                  label={
                    !open
                      ? "This conversation"
                      : projectName
                        ? projectName
                        : "Whole workspace"
                  }
                  project={!!projectName}
                />
              ) : (
                <ScopePicker
                  projectId={dock.projectId}
                  names={names}
                  onScope={dock.setScope}
                />
              )}
              {sendable && (
                <PageChip
                  context={sendable}
                  on={!!sending}
                  onToggle={() =>
                    setDropped(sending ? sendable.path : undefined)
                  }
                />
              )}
            </>
          }
          hint={
            <span className="hidden px-1 sm:inline">
              Enter to send, Shift+Enter for a new line
            </span>
          }
        />
        {overHistory && (
          <div className="absolute inset-0 z-20 flex flex-col bg-background">
            <ConversationHistory
              className="flex-1"
              selected={dock.conversationId}
              onSelect={(c) => {
                dock.select(c.id, c.projectId || undefined)
                setHistoryOpen(false)
              }}
              {...history}
            />
          </div>
        )}
      </div>
    </div>
  )
}

function PlacementMenu({
  mode,
  onMode,
}: {
  mode: DockMode
  onMode: (mode: DockMode) => void
}) {
  const current = modes.find((m) => m.mode === mode) ?? modes[0]
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Where the agent sits"
            title="Where the agent sits"
          />
        }
      >
        {current.icon}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Where the agent sits</DropdownMenuLabel>
          {modes.map((m) => (
            <DropdownMenuItem key={m.mode} onClick={() => onMode(m.mode)}>
              {m.icon}
              {m.label}
              {m.mode === mode && (
                <DropdownMenuShortcut>
                  <CheckIcon />
                </DropdownMenuShortcut>
              )}
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

const chip =
  "inline-flex h-6 max-w-full min-w-0 items-center gap-1 rounded-full px-2 text-[12px]"

/** ScopeChip says what an open conversation is about. Its first message
 * fixed that, so it doesn't change. */
function ScopeChip({ label, project }: { label: string; project: boolean }) {
  return (
    <span
      className={cn(chip, "bg-muted/70 text-foreground")}
      title="A conversation stays about what its first message was about"
    >
      {project ? (
        <FolderSimpleIcon className="size-3.5 shrink-0" />
      ) : (
        <GlobeIcon className="size-3.5 shrink-0" />
      )}
      <span className="truncate">{label}</span>
    </span>
  )
}

/** PageChip offers the page you're on with the next message, and takes it
 * off again. */
function PageChip({
  context,
  on,
  onToggle,
}: {
  context: PageContext
  on: boolean
  onToggle: () => void
}) {
  if (!on)
    return (
      <button
        type="button"
        onClick={onToggle}
        className={cn(
          chip,
          "border border-dashed text-muted-foreground hover:text-foreground"
        )}
      >
        <PlusIcon className="size-3 shrink-0" />
        <span className="truncate">{context.label}</span>
      </button>
    )
  return (
    <span
      className={cn(chip, "bg-brand-tint/70 pr-0.5 text-brand-ink")}
      title={`Sends what you're looking at: ${context.label}`}
    >
      <MapPinIcon className="size-3.5 shrink-0" />
      <span className="truncate">{context.label}</span>
      <button
        type="button"
        aria-label={`Don't send ${context.label}`}
        onClick={onToggle}
        className="grid size-5 shrink-0 place-items-center rounded-full hover:bg-brand-line/40"
      >
        <XIcon className="size-3" />
      </button>
    </span>
  )
}

const WORKSPACE = "workspace"

/** ScopePicker says what a new conversation is about, and changes it. */
function ScopePicker({
  projectId,
  names,
  onScope,
}: {
  projectId?: string
  names: Map<string, string>
  onScope: (projectId: string | undefined) => void
}) {
  return (
    <Select
      value={projectId ?? WORKSPACE}
      onValueChange={(v) => onScope(!v || v === WORKSPACE ? undefined : v)}
    >
      <SelectTrigger
        size="sm"
        aria-label="What the conversation is about"
        className="h-6 max-w-[220px] min-w-0 rounded-full border-transparent bg-muted/70 px-2 text-[12px] hover:bg-muted"
      >
        <SelectValue>
          {(v: string) =>
            v === WORKSPACE
              ? "The whole workspace"
              : `In ${names.get(v) ?? "this project"}`
          }
        </SelectValue>
      </SelectTrigger>
      <SelectContent align="start">
        <SelectItem value={WORKSPACE}>The whole workspace</SelectItem>
        {names.size > 0 && <SelectSeparator />}
        {[...names].map(([id, name]) => (
          <SelectItem key={id} value={id}>
            {name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
