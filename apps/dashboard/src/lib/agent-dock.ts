// How the agent's panel sits in the layout, and how its conversation
// history is filtered and grouped.
//
// The panel's place (docked on the right, floating, or taking the whole
// page), its width and whether it was open are this browser's, kept in
// local storage. So is, per workspace, the conversation it had open (pins
// are the server's, on the conversation). Storage can be missing or refuse (a
// private window, blocked site data): the panel then starts from defaults
// and forgets on reload, and nothing else changes.
import type { Conversation } from "@/client/forge/loomplane/types"

export type DockMode = "docked" | "floating" | "focus"
export type GroupBy = "project" | "date"

export type DockPrefs = {
  mode: DockMode
  /** The docked panel's width, in pixels. */
  width: number
  open: boolean
  groupBy: GroupBy
}

export const DOCK_MIN = 360
export const DOCK_MAX = 760
export const DOCK_DEFAULT = 420
/** The least room the page keeps beside the docked panel. */
const PAGE_MIN = 480

const prefsKey = "loomplane.agent.dock"
const workspaceKey = (orgId: string) => `loomplane.agent.${orgId}`

const defaults: DockPrefs = {
  mode: "docked",
  width: DOCK_DEFAULT,
  open: false,
  groupBy: "project",
}

function read(key: string): unknown {
  try {
    const raw = window.localStorage.getItem(key)
    return raw ? JSON.parse(raw) : undefined
  } catch {
    return undefined
  }
}

function write(key: string, value: unknown) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // Not kept: the panel still works for this visit.
  }
}

/** clampWidth keeps a docked width between the panel's bounds, and leaves
 * the page its room in a window that has it. */
export function clampWidth(width: number, viewport = window.innerWidth) {
  const max = Math.max(DOCK_MIN, Math.min(DOCK_MAX, viewport - PAGE_MIN))
  return Math.round(Math.min(max, Math.max(DOCK_MIN, width)))
}

export function readDockPrefs(): DockPrefs {
  const v = read(prefsKey)
  if (!v || typeof v !== "object") return defaults
  const p = v as Partial<Record<keyof DockPrefs, unknown>>
  return {
    mode:
      p.mode === "docked" || p.mode === "floating" || p.mode === "focus"
        ? p.mode
        : defaults.mode,
    width:
      typeof p.width === "number" && Number.isFinite(p.width)
        ? p.width
        : defaults.width,
    open: p.open === true,
    groupBy: p.groupBy === "date" ? "date" : defaults.groupBy,
  }
}

export function writeDockPrefs(prefs: DockPrefs) {
  write(prefsKey, prefs)
}

/** What the panel keeps for one workspace. */
export type WorkspaceAgent = {
  conversationId?: string
  /** The project a conversation was about, when the panel knew it. */
  projectId?: string
}

export function readWorkspaceAgent(orgId: string): WorkspaceAgent {
  const v = read(workspaceKey(orgId))
  if (!v || typeof v !== "object") return {}
  const w = v as Partial<Record<keyof WorkspaceAgent, unknown>>
  const str = (s: unknown) => (typeof s === "string" && s ? s : undefined)
  return {
    conversationId: str(w.conversationId),
    projectId: str(w.projectId),
  }
}

export function writeWorkspaceAgent(orgId: string, value: WorkspaceAgent) {
  write(workspaceKey(orgId), value)
}

/** The statuses the history filters by, in the words people use. */
export type StatusFilter = "all" | "waiting" | "working" | "failed"

/** Which conversations' project the history shows: every one, only those
 * about the whole workspace, or one project's. */
export type ProjectFilter = "all" | "workspace" | (string & {})

export type HistoryFilter = {
  query: string
  status: StatusFilter
  project: ProjectFilter
}

export const noFilter: HistoryFilter = {
  query: "",
  status: "all",
  project: "all",
}

export function statusFilterOf(status: string): StatusFilter {
  switch (status) {
    case "waiting":
      return "waiting"
    case "queued":
    case "running":
      return "working"
    case "failed":
      return "failed"
    default:
      return "all"
  }
}

/** filterConversations keeps the conversations a filter lets through. The
 * query matches a title or its project's name, ignoring case. */
export function filterConversations(
  list: Conversation[],
  filter: HistoryFilter,
  names: Map<string, string>
): Conversation[] {
  const q = filter.query.trim().toLowerCase()
  return list.filter((c) => {
    if (filter.status !== "all" && statusFilterOf(c.status) !== filter.status)
      return false
    if (filter.project === "workspace" && c.projectId) return false
    if (
      filter.project !== "all" &&
      filter.project !== "workspace" &&
      c.projectId !== filter.project
    )
      return false
    if (!q) return true
    const project = c.projectId ? (names.get(c.projectId) ?? "") : ""
    return (
      (c.title || "untitled").toLowerCase().includes(q) ||
      project.toLowerCase().includes(q)
    )
  })
}

export type HistoryGroup = {
  key: string
  label: string
  conversations: Conversation[]
}

const day = 24 * 60 * 60 * 1000

/** dateGroup names when a conversation was last updated, relative to now
 * in the browser's own days. */
export function dateGroup(updatedAt: string, now: number): string {
  const at = new Date(updatedAt).getTime()
  const today = new Date(now)
  today.setHours(0, 0, 0, 0)
  const start = today.getTime()
  if (at >= start) return "Today"
  if (at >= start - day) return "Yesterday"
  if (at >= start - 6 * day) return "Previous 7 days"
  if (at >= start - 29 * day) return "Previous 30 days"
  return "Older"
}

const dateOrder = [
  "Today",
  "Yesterday",
  "Previous 7 days",
  "Previous 30 days",
  "Older",
]

const newestFirst = (a: Conversation, b: Conversation) =>
  b.updatedAt.localeCompare(a.updatedAt)

/**
 * groupConversations sorts conversations into the history's sections:
 * pinned ones first, then by project (the most recently active project
 * first, the whole workspace among them) or by date. Each section lists
 * its newest conversation first, and empty sections are left out.
 */
export function groupConversations(
  list: Conversation[],
  by: GroupBy,
  names: Map<string, string>,
  pinned: ReadonlySet<string>,
  now: number
): HistoryGroup[] {
  const sorted = [...list].sort(newestFirst)
  const groups: HistoryGroup[] = []
  const pins = sorted.filter((c) => pinned.has(c.id))
  if (pins.length)
    groups.push({ key: "pinned", label: "Pinned", conversations: pins })

  const sections = new Map<string, HistoryGroup>()
  for (const c of sorted) {
    if (pinned.has(c.id)) continue
    const key =
      by === "project"
        ? `project:${c.projectId || ""}`
        : dateGroup(c.updatedAt, now)
    let group = sections.get(key)
    if (!group) {
      const label =
        by === "date"
          ? key
          : !c.projectId
            ? "Whole workspace"
            : (names.get(c.projectId) ?? "A deleted project")
      group = { key, label, conversations: [] }
      sections.set(key, group)
    }
    group.conversations.push(c)
  }
  // By project, sections already follow their newest conversation; by
  // date, they follow the calendar.
  const rest = [...sections.values()]
  if (by === "date")
    rest.sort((a, b) => dateOrder.indexOf(a.key) - dateOrder.indexOf(b.key))
  return [...groups, ...rest]
}
