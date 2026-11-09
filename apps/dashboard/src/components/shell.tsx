// What every page can reach for: the command menu, the new deployment and
// new project dialogs, the agent's panel and the shortcut sheet, plus the
// keyboard handling that opens them. The agent's panel is part of the
// layout (AgentDock): the shell holds what it shows and where it sits.
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import { CommandMenu } from "@/components/command-menu"
import type { DeployPreset } from "@/components/deploy-dialog"
import { NewProjectDialog } from "@/components/new-project-dialog"
import {
  type ShortcutGroup,
  ShortcutsDialog,
} from "@/components/shortcuts-dialog"
import type { OrbState } from "@/components/assistant/agent-orb"
import {
  type DockMode,
  type DockPrefs,
  type GroupBy,
  clampWidth,
  readDockPrefs,
  readWorkspaceAgent,
  writeDockPrefs,
  writeWorkspaceAgent,
} from "@/lib/agent-dock"
import { useAssistantAvailable } from "@/lib/agent-stream"
import { projectIDFromPath } from "@/lib/command-menu"
import { useOrganization } from "@/lib/organization"
import { useNavigate, usePath } from "@/lib/router"

/** Where openAssistant opens the agent's panel. */
export type AssistantTarget = {
  projectId?: string
  conversationId?: string
  /** Where the panel goes; it stays where it was without one. */
  mode?: DockMode
}

type Shell = {
  openAssistant: (opts?: AssistantTarget) => void
  openCommand: () => void
  openDeploy: (preset?: DeployPreset) => void
  openNewProject: () => void
  openShortcuts: () => void
}

/** The agent's panel, as the layout, the sidebar and the panel see it. */
export type AgentDock = DockPrefs & {
  /** The project a new conversation is about; none is the whole
   * workspace. An open conversation's own project is in its record. */
  projectId?: string
  /** The open conversation; none is a new one. */
  conversationId?: string
  /** What the agent is doing in the panel, as its orb shows it. */
  activity: OrbState
  setActivity: (state: OrbState) => void
  setOpen: (open: boolean) => void
  toggle: () => void
  setMode: (mode: DockMode) => void
  setWidth: (width: number) => void
  setGroupBy: (by: GroupBy) => void
  /** select opens a conversation from the history, with the project it
   * is about. */
  select: (conversationId: string, projectId?: string) => void
  /** newConversation leaves the open one for a new one about the page
   * you're on. */
  newConversation: () => void
  /** setScope changes what the next new conversation is about. */
  setScope: (projectId: string | undefined) => void
  /** onConversation hears the ID a new conversation got. */
  onConversation: (id: string) => void
}

const AgentDockContext = createContext<AgentDock | null>(null)

/** useAgentDock is the agent's panel, or null where there's no agent to
 * talk to (AI off, the workspace's agent off, or no permission). */
export function useAgentDock(): AgentDock | null {
  return useContext(AgentDockContext)
}

const ShellContext = createContext<Shell | null>(null)

export function useShell(): Shell {
  const s = useContext(ShellContext)
  if (!s) throw new Error("useShell outside ShellProvider")
  return s
}

// How long after G the second key still counts as a "go to" shortcut.
const sequenceWindow = 1200

const goTo = {
  o: "/",
  d: "/deployments",
  p: "/projects",
  s: "/services",
  c: "/clouds",
  i: "/infrastructure",
  a: "/activity",
  v: "/variables",
  m: "/members",
  k: "/settings/api-keys",
} as const

// releaseFocus takes focus out of the agent's panel as it closes: a hidden
// box would keep it and swallow the page's shortcuts.
function releaseFocus() {
  const el = document.activeElement
  if (el instanceof HTMLElement && el.closest("[data-agent-dock]")) el.blur()
}

function isEditable(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false
  return (
    target.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
    target.closest("[role=dialog], [role=menu], [role=listbox]") !== null
  )
}

export function ShellProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate()
  const { can, session } = useOrganization()
  const mayDeploy = can("deployment:create")
  const [command, setCommand] = useState(false)
  const [newProject, setNewProject] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const pendingG = useRef(0)
  const path = usePath()
  const assistantOn = useAssistantAvailable()
  const orgId = session.orgId
  const [prefs, setPrefs] = useState(readDockPrefs)
  const [saved] = useState(() => readWorkspaceAgent(orgId))
  const [assistant, setAssistant] = useState<{
    // The open conversation's project, or the next new one's.
    projectId?: string
    conversationId?: string
    // The project page the panel last followed.
    from?: string
  }>(() => ({
    conversationId: saved.conversationId,
    projectId: saved.projectId,
  }))
  const open = assistantOn && prefs.open
  const [activity, setActivity] = useState<OrbState>("idle")

  useEffect(() => writeDockPrefs(prefs), [prefs])
  useEffect(
    () =>
      writeWorkspaceAgent(orgId, {
        conversationId: assistant.conversationId,
        projectId: assistant.projectId,
      }),
    [orgId, assistant.conversationId, assistant.projectId]
  )

  // The panel stays open as you move around. With no conversation open, a
  // new one is about the project page you're on; a conversation's project
  // is fixed by its first message, so an open one stays as it is.
  const here = projectIDFromPath(path)
  if (open && here !== assistant.from && !assistant.conversationId)
    setAssistant((cur) => ({ ...cur, from: here, projectId: here }))

  // The panel keeps its conversation and scope between openings. Opened
  // from another project's page (or from none, after one) with no
  // conversation open, it takes that page's project for the next one.
  const openAssistant = useCallback(
    (opts?: AssistantTarget) => {
      if (!assistantOn) return
      setPrefs((p) => ({ ...p, open: true, mode: opts?.mode ?? p.mode }))
      setAssistant((cur) => {
        const next = { ...cur, from: here }
        if (opts?.conversationId)
          return {
            ...next,
            conversationId: opts.conversationId,
            projectId: opts.projectId,
          }
        // Asked for a project by name: a conversation about another one
        // gives way to a new one there.
        if (opts?.projectId)
          return opts.projectId === cur.projectId
            ? next
            : { ...next, projectId: opts.projectId, conversationId: undefined }
        if (here !== cur.from && !cur.conversationId)
          return { ...next, projectId: here }
        return next
      })
    },
    [assistantOn, here]
  )
  const closeAssistant = useCallback(() => {
    releaseFocus()
    setPrefs((p) => ({ ...p, open: false }))
  }, [])
  const toggleAssistant = useCallback(() => {
    if (open) closeAssistant()
    else openAssistant()
  }, [open, openAssistant, closeAssistant])

  const dock = useMemo<AgentDock | null>(
    () =>
      assistantOn
        ? {
            ...prefs,
            open,
            projectId: assistant.projectId,
            conversationId: assistant.conversationId,
            activity,
            setActivity,
            setOpen: (o) => (o ? openAssistant() : closeAssistant()),
            toggle: toggleAssistant,
            setMode: (mode) => setPrefs((p) => ({ ...p, mode })),
            setWidth: (width) =>
              setPrefs((p) => ({ ...p, width: clampWidth(width) })),
            setGroupBy: (groupBy) => setPrefs((p) => ({ ...p, groupBy })),
            select: (conversationId, projectId) =>
              setAssistant((cur) => ({ ...cur, conversationId, projectId })),
            newConversation: () =>
              setAssistant({ from: here, projectId: here }),
            setScope: (projectId) =>
              setAssistant((cur) => ({ ...cur, projectId })),
            onConversation: (id) =>
              setAssistant((cur) => ({ ...cur, conversationId: id })),
          }
        : null,
    [
      assistantOn,
      prefs,
      open,
      assistant.projectId,
      assistant.conversationId,
      activity,
      openAssistant,
      closeAssistant,
      toggleAssistant,
      here,
    ]
  )

  const openCommand = useCallback(() => setCommand(true), [])
  const openNewProject = useCallback(() => setNewProject(true), [])
  const openDeploy = useCallback(
    (preset?: DeployPreset) => {
      if (mayDeploy)
        void navigate({
          to: "/deployments/new",
          search: {
            project: preset?.project,
            environment: preset?.environment,
          },
        })
    },
    [mayDeploy, navigate]
  )
  const openShortcuts = useCallback(() => setShortcuts(true), [])

  useEffect(() => {
    // Capture phase, so a "g" sequence is ours before the theme provider's
    // bare "d" toggle sees its second key.
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault()
        setCommand((open) => !open)
        return
      }
      // With the agent unavailable, ⌘J stays the browser's.
      if (
        (e.metaKey || e.ctrlKey) &&
        !e.altKey &&
        !e.shiftKey &&
        e.key.toLowerCase() === "j" &&
        assistantOn
      ) {
        e.preventDefault()
        toggleAssistant()
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey || isEditable(e.target)) return
      const key = e.key.toLowerCase()
      if (
        Date.now() - pendingG.current < sequenceWindow &&
        Object.hasOwn(goTo, key)
      ) {
        e.preventDefault()
        e.stopPropagation()
        pendingG.current = 0
        void navigate({ to: goTo[key as keyof typeof goTo] })
        return
      }
      if (key === "g") {
        pendingG.current = Date.now()
        return
      }
      if (e.key === "?") {
        e.preventDefault()
        setShortcuts(true)
      } else if (key === "n" && mayDeploy) {
        e.preventDefault()
        openDeploy()
      } else if (key === "/") {
        const search =
          document.querySelector<HTMLInputElement>("[data-page-search]")
        if (search) {
          e.preventDefault()
          search.focus()
        }
      }
    }
    window.addEventListener("keydown", onKey, { capture: true })
    return () => window.removeEventListener("keydown", onKey, { capture: true })
  }, [navigate, mayDeploy, openDeploy, assistantOn, toggleAssistant])

  const value = useMemo(
    () => ({
      openAssistant,
      openCommand,
      openDeploy,
      openNewProject,
      openShortcuts,
    }),
    [openAssistant, openCommand, openDeploy, openNewProject, openShortcuts]
  )

  const groups: ShortcutGroup[] = [
    {
      title: "Anywhere",
      keys: [
        [["⌘", "K"], "Open the command menu"],
        ...(assistantOn
          ? ([[["⌘", "J"], "Ask the agent"]] as [string[], string][])
          : []),
        ...(mayDeploy
          ? ([[["N"], "New deployment"]] as [string[], string][])
          : []),
        [["/"], "Search the current page"],
        [["⌘", "B"], "Collapse the sidebar"],
        [["D"], "Switch light and dark"],
        [["?"], "Show these shortcuts"],
      ],
    },
    {
      title: "Go to",
      keys: [
        [["G", "O"], "Overview"],
        [["G", "D"], "Deployments"],
        [["G", "P"], "Projects"],
        [["G", "S"], "Services"],
        [["G", "C"], "Clouds"],
        [["G", "I"], "Infrastructure"],
        [["G", "A"], "Activity"],
        [["G", "V"], "Variables"],
        [["G", "M"], "Members"],
        [["G", "K"], "API keys"],
      ],
    },
  ]

  return (
    <ShellContext.Provider value={value}>
      <AgentDockContext.Provider value={dock}>
        {children}
      </AgentDockContext.Provider>
      <CommandMenu open={command} onOpenChange={setCommand} />
      <NewProjectDialog open={newProject} onOpenChange={setNewProject} />
      <ShortcutsDialog
        groups={groups}
        open={shortcuts}
        onOpenChange={setShortcuts}
      />
    </ShellContext.Provider>
  )
}
