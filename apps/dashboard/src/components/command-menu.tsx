// The command menu (⌘K): go anywhere, open any project, run an action.
import {
  BracesIcon,
  FolderGit2Icon,
  FolderPlusIcon,
  KeyboardIcon,
  MoonIcon,
  PlugIcon,
  ScanSearchIcon,
  ServerIcon,
  UsersIcon,
} from "lucide-react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesCreate,
  useAnalysesList,
  useProjectsGraph,
  useProjectsList,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { useShell } from "@/components/shell"
import { useTheme } from "@/components/theme-provider"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/components/ui/command"
import { analyzeItems, projectIDFromPath } from "@/lib/command-menu"
import { repositoriesLabel } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { useNavigate, usePath } from "@/lib/router"

/**
 * RepositoryActions offers to analyze the repositories of the project the
 * page is on. It is only mounted while the menu is open, so the sources are
 * only loaded then.
 */
function RepositoryActions({
  projectID,
  onAnalyze,
}: {
  projectID: string
  onAnalyze: (projectID: string, alias?: string) => void
}) {
  const { can } = useOrganization()
  const sources = useQuery(useSourcesList, { path: { id: projectID } })
  const items = analyzeItems(sources.data?.sources ?? [], can("analysis:run"))
  if (items.length === 0) return null
  return (
    <CommandGroup heading="Repositories">
      {items.map((item) => (
        <CommandItem
          key={item.key}
          value={item.search}
          onSelect={() => onAnalyze(projectID, item.alias)}
        >
          <ScanSearchIcon />
          {item.label}
          <span className="truncate text-muted-foreground">{item.detail}</span>
        </CommandItem>
      ))}
    </CommandGroup>
  )
}

export function CommandMenu({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  const { openNewProject, openShortcuts } = useShell()
  const { theme, setTheme } = useTheme()
  const list = useQuery(useProjectsList)
  const projects = list.data?.projects ?? []
  const projectID = projectIDFromPath(usePath())
  const start = useMutation(useAnalysesCreate)
  const invalidate = useInvalidate()

  // The mutation lives here rather than in the group, so it outlasts the
  // menu closing. The toast says how it went.
  const analyze = (id: string, alias?: string) => {
    onOpenChange(false)
    start
      .mutateAsync({
        path: { id },
        body: alias ? { sources: [alias] } : {},
      })
      .then(() => {
        invalidate(useAnalysesList, { path: { id } })
        invalidate(useProjectsGraph, { path: { id } })
        toast.success(
          alias ? `Analyzing ${alias}` : "Analyzing every repository"
        )
      })
      .catch((err: unknown) =>
        toast.error(messageOf(err, "The analysis didn't start."))
      )
  }

  const run = (fn: () => void) => () => {
    onOpenChange(false)
    fn()
  }

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Command menu"
      description="Go anywhere or run an action"
    >
      <Command>
        <CommandInput placeholder="Search projects, pages and actions" />
        <CommandList>
          <CommandEmpty>Nothing matches. Try a project name.</CommandEmpty>
          <CommandGroup heading="Go to">
            <CommandItem
              value="projects home"
              onSelect={run(() => void navigate({ to: "/" }))}
            >
              <FolderGit2Icon />
              Projects
              <CommandShortcut>G P</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="infrastructure providers clouds"
              onSelect={run(() => void navigate({ to: "/infrastructure" }))}
            >
              <ServerIcon />
              Infrastructure
              <CommandShortcut>G I</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="variables secrets organization"
              onSelect={run(() => void navigate({ to: "/variables" }))}
            >
              <BracesIcon />
              Variables
              <CommandShortcut>G V</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="members invitations people"
              onSelect={run(() => void navigate({ to: "/members" }))}
            >
              <UsersIcon />
              Members
              <CommandShortcut>G M</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="integrations github"
              onSelect={run(() => void navigate({ to: "/integrations" }))}
            >
              <PlugIcon />
              Integrations
            </CommandItem>
          </CommandGroup>
          {projects.length > 0 && (
            <CommandGroup heading="Projects">
              {projects.map((p) => {
                const style = projectStyle(p)
                return (
                  <CommandItem
                    key={p.id}
                    value={`project ${p.name} ${p.slug} ${p.description} ${p.repositories.join(" ")}`}
                    onSelect={run(
                      () =>
                        void navigate({
                          to: "/projects/$id",
                          params: { id: p.id },
                        })
                    )}
                  >
                    <ProjectIcon
                      icon={style.icon}
                      color={style.color}
                      size="xs"
                    />
                    {p.name}
                    <span className="truncate text-muted-foreground">
                      {p.description || repositoriesLabel(p.repositories)}
                    </span>
                  </CommandItem>
                )
              })}
            </CommandGroup>
          )}
          {projectID && can("analysis:run") && (
            <RepositoryActions projectID={projectID} onAnalyze={analyze} />
          )}
          <CommandGroup heading="Actions">
            {can("project:create") && (
              <CommandItem
                value="new project create repository"
                onSelect={run(openNewProject)}
              >
                <FolderPlusIcon />
                New project
                <CommandShortcut>N</CommandShortcut>
              </CommandItem>
            )}
            <CommandItem
              value="connect github integration"
              onSelect={run(() => void navigate({ to: "/integrations" }))}
            >
              <PlugIcon />
              Connect GitHub
            </CommandItem>
            <CommandItem
              value="toggle theme dark light"
              onSelect={run(() =>
                setTheme(theme === "dark" ? "light" : "dark")
              )}
            >
              <MoonIcon />
              Toggle theme
              <CommandShortcut>D</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="keyboard shortcuts help"
              onSelect={run(openShortcuts)}
            >
              <KeyboardIcon />
              Keyboard shortcuts
              <CommandShortcut>?</CommandShortcut>
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  )
}
