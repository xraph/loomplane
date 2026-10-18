// What every page can reach for: the command menu, the new deployment and
// new project dialogs and the shortcut sheet, plus the keyboard handling
// that opens them.
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
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"

type Shell = {
  openCommand: () => void
  openDeploy: (preset?: DeployPreset) => void
  openNewProject: () => void
  openShortcuts: () => void
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
  i: "/infrastructure",
  a: "/activity",
  v: "/variables",
  m: "/members",
} as const

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
  const { can } = useOrganization()
  const mayDeploy = can("deployment:create")
  const [command, setCommand] = useState(false)
  const [newProject, setNewProject] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const pendingG = useRef(0)

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
  }, [navigate, mayDeploy, openDeploy])

  const value = useMemo(
    () => ({ openCommand, openDeploy, openNewProject, openShortcuts }),
    [openCommand, openDeploy, openNewProject, openShortcuts]
  )

  const groups: ShortcutGroup[] = [
    {
      title: "Anywhere",
      keys: [
        [["⌘", "K"], "Open the command menu"],
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
        [["G", "I"], "Infrastructure"],
        [["G", "A"], "Activity"],
        [["G", "V"], "Variables"],
        [["G", "M"], "Members"],
      ],
    },
  ]

  return (
    <ShellContext.Provider value={value}>
      {children}
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
