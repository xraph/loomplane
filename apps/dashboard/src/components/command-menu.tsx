// The command menu (⌘K): go anywhere, open any project, run an action.
import {
  ActivityIcon,
  BellIcon,
  BracesIcon,
  CloudIcon,
  DatabaseIcon,
  FolderGit2Icon,
  FolderPlusIcon,
  LayoutDashboardIcon,
  RocketIcon,
  ShieldCheckIcon,
  KeyRoundIcon,
  KeyboardIcon,
  MoonIcon,
  PlugIcon,
  ScanSearchIcon,
  ServerIcon,
  UsersIcon,
} from "lucide-react"
import { type ReactNode, useState } from "react"
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
import { RunDot } from "@/components/runs"
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
import { changesLine, commitLabel, type Run } from "@/lib/runs"
import { cn } from "@/lib/utils"
import { useRuns } from "@/lib/use-runs"

// Typing searches the newest few runs too; the rest are on Deployments.
const recentShown = 6

/** Glyph is an item's icon on a small tile, like the mock's. */
function Glyph({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <span
      className={cn(
        "flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground group-data-selected/command-item:bg-background group-data-selected/command-item:text-foreground [&_svg:not([class*='size-'])]:size-3.5",
        className
      )}
    >
      {children}
    </span>
  )
}

/** Entry is one row: icon, a label that truncates, a hint and its keys. */
function Entry({
  icon,
  label,
  hint,
  keys,
  glyphClassName,
}: {
  icon: ReactNode
  label: ReactNode
  hint?: ReactNode
  keys?: string
  glyphClassName?: string
}) {
  return (
    <>
      <Glyph className={glyphClassName}>{icon}</Glyph>
      <span className="grid min-w-0 flex-1 gap-px">
        <span className="truncate font-medium">{label}</span>
        {hint && (
          <span className="truncate text-xs text-muted-foreground">{hint}</span>
        )}
      </span>
      {keys && <CommandShortcut>{keys}</CommandShortcut>}
    </>
  )
}

/**
 * RunGroups offers the plans waiting on someone, and the latest runs once
 * you type. Only mounted while the menu is open.
 */
function RunGroups({
  go,
  searching,
}: {
  go: (r: Run) => void
  searching: boolean
}) {
  const { can } = useOrganization()
  const { runs } = useRuns()
  const waiting = runs.filter((r) => r.status === "waiting")
  return (
    <>
      {waiting.length > 0 && can("deployment:approve") && (
        <CommandGroup heading="Needs you">
          {waiting.map((r) => (
            <CommandItem
              key={r.id}
              value={`approve review plan ${r.project?.name ?? ""} ${r.environment} ${r.id}`}
              onSelect={() => go(r)}
            >
              <Entry
                icon={<ShieldCheckIcon />}
                glyphClassName="bg-weld-tint text-weld-ink"
                label={`Review the plan for ${r.project?.name ?? "a project"} to ${r.environment}`}
                hint={changesLine(r)}
              />
            </CommandItem>
          ))}
        </CommandGroup>
      )}
      {searching && runs.length > 0 && (
        <CommandGroup heading="Deployments">
          {runs.slice(0, recentShown).map((r) => (
            <CommandItem
              key={r.id}
              value={`deployment ${r.id} ${r.deployment?.id ?? ""} ${r.project?.name ?? ""} ${r.environment} ${commitLabel(r)}`}
              onSelect={() => go(r)}
            >
              <Entry
                icon={<RunDot status={r.status} />}
                label={r.project?.name ?? "A deleted project"}
                hint={[
                  <span key="env" className="capitalize">
                    {r.environment}
                  </span>,
                  " · ",
                  r.phase,
                  commitLabel(r) ? ` · ${commitLabel(r)}` : "",
                ]}
              />
            </CommandItem>
          ))}
        </CommandGroup>
      )}
    </>
  )
}

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
          <Entry
            icon={<ScanSearchIcon />}
            label={item.label}
            hint={item.detail}
          />
        </CommandItem>
      ))}
    </CommandGroup>
  )
}

type Page = {
  to:
    | "/"
    | "/deployments"
    | "/projects"
    | "/services"
    | "/clouds"
    | "/infrastructure"
    | "/activity"
    | "/variables"
    | "/members"
    | "/notifications"
    | "/integrations"
    | "/api-keys"
  label: string
  icon: ReactNode
  search: string
  keys?: string
}

// The same order and chords as the sidebar and the shortcuts dialog.
const pages: Page[] = [
  {
    to: "/",
    label: "Overview",
    icon: <LayoutDashboardIcon />,
    search: "overview home",
    keys: "G O",
  },
  {
    to: "/deployments",
    label: "Deployments",
    icon: <RocketIcon />,
    search: "deployments plans runs",
    keys: "G D",
  },
  {
    to: "/projects",
    label: "Projects",
    icon: <FolderGit2Icon />,
    search: "projects",
    keys: "G P",
  },
  {
    to: "/services",
    label: "Services",
    icon: <DatabaseIcon />,
    search: "services catalog databases caches postgres redis",
    keys: "G S",
  },
  {
    to: "/clouds",
    label: "Clouds",
    icon: <CloudIcon />,
    search: "clouds targets accounts",
    keys: "G C",
  },
  {
    to: "/infrastructure",
    label: "Infrastructure",
    icon: <ServerIcon />,
    search: "infrastructure providers",
    keys: "G I",
  },
  {
    to: "/activity",
    label: "Activity",
    icon: <ActivityIcon />,
    search: "activity deployments metrics drift",
    keys: "G A",
  },
  {
    to: "/variables",
    label: "Variables",
    icon: <BracesIcon />,
    search: "variables secrets organization",
    keys: "G V",
  },
  {
    to: "/members",
    label: "Members",
    icon: <UsersIcon />,
    search: "members invitations people",
    keys: "G M",
  },
  {
    to: "/notifications",
    label: "Notifications",
    icon: <BellIcon />,
    search: "notifications inbox email webhooks",
  },
  {
    to: "/integrations",
    label: "Integrations",
    icon: <PlugIcon />,
    search: "integrations github catalog",
  },
  {
    to: "/api-keys",
    label: "API keys",
    icon: <KeyRoundIcon />,
    search: "api keys tokens cli",
    keys: "G K",
  },
]

export function CommandMenu({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  const { openDeploy, openNewProject, openShortcuts } = useShell()
  const { theme, setTheme } = useTheme()
  const list = useQuery(useProjectsList)
  const projects = list.data?.projects ?? []
  const projectID = projectIDFromPath(usePath())
  const start = useMutation(useAnalysesCreate)
  const invalidate = useInvalidate()
  const [search, setSearch] = useState("")

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
      onOpenChange={(next) => {
        if (!next) setSearch("")
        onOpenChange(next)
      }}
      title="Command menu"
      description="Go anywhere or run an action"
    >
      <Command>
        <CommandInput
          placeholder="Search projects, pages and actions"
          value={search}
          onValueChange={setSearch}
        />
        <CommandList>
          <CommandEmpty>Nothing matches. Try a project name.</CommandEmpty>
          <RunGroups
            searching={search.trim() !== ""}
            go={(r) => {
              onOpenChange(false)
              void navigate({ to: "/deployments/$id", params: { id: r.id } })
            }}
          />
          <CommandGroup heading="Go to">
            {pages.map((p) => (
              <CommandItem
                key={p.to}
                value={p.search}
                onSelect={run(() => void navigate({ to: p.to }))}
              >
                <Entry icon={p.icon} label={p.label} keys={p.keys} />
              </CommandItem>
            ))}
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
                    <span className="flex size-7 shrink-0 items-center justify-center">
                      <ProjectIcon
                        icon={style.icon}
                        color={style.color}
                        size="sm"
                      />
                    </span>
                    <span className="grid min-w-0 flex-1 gap-px">
                      <span className="truncate font-medium">{p.name}</span>
                      {(p.repositories.length > 0 || p.description) && (
                        <span className="truncate text-xs text-muted-foreground">
                          {p.repositories.length
                            ? repositoriesLabel(p.repositories)
                            : p.description}
                        </span>
                      )}
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
            {can("deployment:create") && (
              <CommandItem
                value="new deployment deploy plan"
                onSelect={run(() => openDeploy())}
              >
                <Entry icon={<RocketIcon />} label="New deployment" keys="N" />
              </CommandItem>
            )}
            {can("project:create") && (
              <CommandItem
                value="new project create repository"
                onSelect={run(openNewProject)}
              >
                <Entry icon={<FolderPlusIcon />} label="New project" />
              </CommandItem>
            )}
            <CommandItem
              value="connect github integration"
              onSelect={run(() => void navigate({ to: "/integrations" }))}
            >
              <Entry icon={<PlugIcon />} label="Connect GitHub" />
            </CommandItem>
            <CommandItem
              value="toggle theme dark light"
              onSelect={run(() =>
                setTheme(theme === "dark" ? "light" : "dark")
              )}
            >
              <Entry icon={<MoonIcon />} label="Toggle theme" keys="D" />
            </CommandItem>
            <CommandItem
              value="keyboard shortcuts help"
              onSelect={run(openShortcuts)}
            >
              <Entry
                icon={<KeyboardIcon />}
                label="Keyboard shortcuts"
                keys="?"
              />
            </CommandItem>
          </CommandGroup>
        </CommandList>
        <div className="flex items-center gap-4 border-t bg-subtle px-4 py-2.5 text-xs text-muted-foreground">
          <span className="flex items-center gap-1.5">
            <kbd className="rounded border px-1 font-mono text-[10.5px]">
              ↑↓
            </kbd>
            Move
          </span>
          <span className="flex items-center gap-1.5">
            <kbd className="rounded border px-1 font-mono text-[10.5px]">↵</kbd>
            Open
          </span>
          <span className="flex-1" />
          <span className="max-sm:hidden">Type a project, page or plan ID</span>
        </div>
      </Command>
    </CommandDialog>
  )
}
