import {
  CheckCircle2Icon,
  CloudIcon,
  FolderPlusIcon,
  KeyboardIcon,
  LayoutDashboardIcon,
  MoonIcon,
  RocketIcon,
  ServerIcon,
  TerminalIcon,
} from "lucide-react"
import { toast } from "sonner"

import { useTheme } from "@/components/theme-provider"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from "@/components/ui/command"
import { StatusDot } from "@/mock/components/bits"
import { ProjectIcon } from "@/lib/project-style"
import { navigate } from "@/mock/router"
import { useShell } from "@/mock/shell"
import { useStore } from "@/mock/store"

export function CommandMenu({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { deployments, approve, projects } = useStore()
  const { openDeploy, openNewProject, openShortcuts } = useShell()
  const { theme, setTheme } = useTheme()
  const waiting = deployments.filter((d) => d.status === "waiting")

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
        <CommandInput placeholder="Search projects, deployments and actions" />
        <CommandList>
          <CommandEmpty>
            Nothing matches. Try a project name or a SHA.
          </CommandEmpty>
          {waiting.length > 0 && (
            <CommandGroup heading="Needs you">
              {waiting.map((d) => (
                <CommandItem
                  key={d.id}
                  value={`approve ${d.project} ${d.env} ${d.id}`}
                  onSelect={run(() => {
                    approve(d.id)
                    toast.success(
                      `Approved the plan for ${d.project} on ${d.env}`
                    )
                  })}
                >
                  <CheckCircle2Icon />
                  Approve plan for {d.project} on {d.env}
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          <CommandGroup heading="Go to">
            <CommandItem value="overview" onSelect={run(() => navigate("/"))}>
              <LayoutDashboardIcon />
              Overview
              <CommandShortcut>G O</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="deployments"
              onSelect={run(() => navigate("/deployments"))}
            >
              <RocketIcon />
              Deployments
              <CommandShortcut>G D</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="infrastructure clouds resources"
              onSelect={run(() => navigate("/infrastructure"))}
            >
              <ServerIcon />
              Infrastructure
              <CommandShortcut>G I</CommandShortcut>
            </CommandItem>
          </CommandGroup>
          <CommandGroup heading="Projects">
            {projects.map((p) => (
              <CommandItem
                key={p.id}
                value={`project ${p.name} ${p.description} ${p.repo}`}
                onSelect={run(() => navigate(`/projects/${p.id}`))}
              >
                <ProjectIcon icon={p.icon} color={p.color} size="xs" />
                {p.name}
                <span className="truncate text-muted-foreground">
                  {p.description}
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
          <CommandGroup heading="Actions">
            <CommandItem
              value="new deployment deploy"
              onSelect={run(() => openDeploy())}
            >
              <RocketIcon />
              New deployment
              <CommandShortcut>N</CommandShortcut>
            </CommandItem>
            <CommandItem
              value="new project create import repository"
              onSelect={run(openNewProject)}
            >
              <FolderPlusIcon />
              New project
            </CommandItem>
            <CommandItem
              value="connect a cloud provider"
              onSelect={run(() => navigate("/infrastructure"))}
            >
              <CloudIcon />
              Connect a cloud
            </CommandItem>
            <CommandItem
              value="copy cli install command"
              onSelect={run(() => {
                void navigator.clipboard?.writeText(
                  "curl -fsSL https://loomplane.dev/install.sh | sh"
                )
                toast.success("Copied the CLI install command")
              })}
            >
              <TerminalIcon />
              Copy CLI install command
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
          <CommandSeparator />
          <CommandGroup heading="Recent deployments">
            {deployments.slice(0, 6).map((d) => (
              <CommandItem
                key={d.id}
                value={`deployment ${d.id} ${d.project} ${d.env} ${d.sha} ${d.message}`}
                onSelect={run(() => navigate(`/deployments/${d.id}`))}
              >
                <StatusDot status={d.status} />
                <span className="font-medium">{d.project}</span>
                <span className="truncate text-muted-foreground">
                  {d.message}
                </span>
                <CommandShortcut className="font-mono">{d.sha}</CommandShortcut>
              </CommandItem>
            ))}
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  )
}
