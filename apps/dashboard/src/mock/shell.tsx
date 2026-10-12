// What every page can reach for: the command menu, the deploy and new project
// dialogs and the shortcut sheet, plus the keyboard handling that opens them.
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

import { CommandMenu } from "@/mock/components/command-menu"
import { DeployDialog } from "@/mock/components/deploy-dialog"
import { NewProjectDialog } from "@/mock/components/new-project-dialog"
import {
  type ShortcutGroup,
  ShortcutsDialog,
} from "@/components/shortcuts-dialog"
import { navigate } from "@/mock/router"
import type { DeployRequest } from "@/mock/store"

type Shell = {
  openCommand: () => void
  openDeploy: (preset?: Partial<DeployRequest>) => void
  openNewProject: () => void
  openShortcuts: () => void
}

const ShellContext = createContext<Shell | null>(null)

export function useShell(): Shell {
  const s = useContext(ShellContext)
  if (!s) throw new Error("useShell outside ShellProvider")
  return s
}

function isEditable(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false
  return (
    target.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
    target.closest("[role=dialog], [role=menu], [role=listbox]") !== null
  )
}

const shortcutGroups: ShortcutGroup[] = [
  {
    title: "Anywhere",
    keys: [
      [["⌘", "K"], "Open the command menu"],
      [["N"], "New deployment"],
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
      [["G", "I"], "Infrastructure"],
      [["G", "P"], "The atlas project"],
    ],
  },
]

const goTo: Record<string, string> = {
  o: "/",
  d: "/deployments",
  i: "/infrastructure",
  p: "/projects/atlas",
}

export function ShellProvider({ children }: { children: ReactNode }) {
  const [command, setCommand] = useState(false)
  const [deploy, setDeploy] = useState<Partial<DeployRequest> | null>(null)
  const [newProject, setNewProject] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const pendingG = useRef(0)

  const openCommand = useCallback(() => setCommand(true), [])
  const openDeploy = useCallback(
    (preset?: Partial<DeployRequest>) => setDeploy(preset ?? {}),
    []
  )
  const openNewProject = useCallback(() => setNewProject(true), [])
  const openShortcuts = useCallback(() => setShortcuts(true), [])

  useEffect(() => {
    // Capture phase, so a "g d" sequence is ours before the theme
    // provider's bare "d" toggle sees it.
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault()
        setCommand((open) => !open)
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey || isEditable(e.target)) return
      const key = e.key.toLowerCase()
      if (Date.now() - pendingG.current < 1200 && goTo[key]) {
        e.preventDefault()
        e.stopPropagation()
        pendingG.current = 0
        navigate(goTo[key])
        return
      }
      if (key === "g") {
        pendingG.current = Date.now()
        return
      }
      if (e.key === "?") {
        e.preventDefault()
        setShortcuts(true)
      } else if (key === "n") {
        e.preventDefault()
        setDeploy({})
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
  }, [])

  const value = useMemo(
    () => ({ openCommand, openDeploy, openNewProject, openShortcuts }),
    [openCommand, openDeploy, openNewProject, openShortcuts]
  )

  return (
    <ShellContext.Provider value={value}>
      {children}
      <CommandMenu open={command} onOpenChange={setCommand} />
      <DeployDialog
        preset={deploy}
        onOpenChange={(open) => {
          if (!open) setDeploy(null)
        }}
      />
      <NewProjectDialog open={newProject} onOpenChange={setNewProject} />
      <ShortcutsDialog
        groups={shortcutGroups}
        open={shortcuts}
        onOpenChange={setShortcuts}
      />
    </ShellContext.Provider>
  )
}
