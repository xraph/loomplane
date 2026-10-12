// From shadcn's sidebar-07 block (app-sidebar.tsx): a sidebar that collapses
// to icons, with a team switcher, collapsible navigation, projects and the
// user menu. The sample data is replaced with the mock's live state.
import {
  BellIcon,
  CopyIcon,
  FolderGit2Icon,
  KeyboardIcon,
  KeyRoundIcon,
  LayoutDashboardIcon,
  PlusIcon,
  RocketIcon,
  ServerIcon,
  Settings2Icon,
  TerminalIcon,
  UserIcon,
} from "lucide-react"
import type { ComponentProps } from "react"
import { toast } from "sonner"

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar"
import { NavMain, type NavItem } from "@/components/sidebar/nav-main"
import { type Attention, NavProjects } from "@/components/sidebar/nav-projects"
import { NavUser } from "@/components/sidebar/nav-user"
import { TeamSwitcher } from "@/components/sidebar/team-switcher"
import {
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
} from "@/components/ui/dropdown-menu"
import { me, organization } from "@/mock/data"
import { to, useQuery, useRoute } from "@/mock/router"
import { useShell } from "@/mock/shell"
import { type DeploymentStatus, isLive, useStore } from "@/mock/store"

// The dot on a project's tile: only for what needs a look.
function attentionFor(status?: DeploymentStatus): Attention | undefined {
  if (status === "running") return { tone: "moving", note: "deploying" }
  if (status === "waiting") {
    return { tone: "waiting", note: "waiting for approval" }
  }
  if (status === "failed") return { tone: "failed", note: "last deploy failed" }
  return undefined
}

const notMocked = (what: string) => () =>
  toast(`${what} isn't part of the mock yet`)

function scrollTo(id: string) {
  return () => {
    if (window.location.hash.split("?")[0] !== "#/infrastructure") {
      window.location.hash = "/infrastructure"
    }
    window.setTimeout(
      () =>
        document
          .getElementById(id)
          ?.scrollIntoView({ behavior: "smooth", block: "start" }),
      80
    )
  }
}

export function AppSidebar(props: ComponentProps<typeof Sidebar>) {
  const route = useRoute()
  const query = useQuery()
  const { deployments, projects } = useStore()
  const { openDeploy, openNewProject, openShortcuts } = useShell()
  const live = deployments.filter(isLive).length
  const waiting = deployments.filter((d) => d.status === "waiting").length
  const failed = deployments.filter((d) => d.status === "failed").length
  const status = route === "/deployments" ? (query.get("status") ?? "all") : ""

  const nav: NavItem[] = [
    {
      title: "Overview",
      link: <a href={to("/")} />,
      icon: <LayoutDashboardIcon />,
      isActive: route === "/",
    },
    {
      title: "Deployments",
      link: <a href={to("/deployments")} />,
      icon: <RocketIcon />,
      isActive: route.startsWith("/deployments"),
      items: [
        {
          title: "All deployments",
          link: <a href={to("/deployments")} />,
          isActive: status === "all",
        },
        {
          title: "In flight",
          link: <a href={to("/deployments?status=live")} />,
          isActive: status === "live",
          badge: live || undefined,
        },
        {
          title: "Needs approval",
          link: <a href={to("/deployments?status=waiting")} />,
          isActive: status === "waiting",
          badge: waiting || undefined,
        },
        {
          title: "Failed",
          link: <a href={to("/deployments?status=failed")} />,
          isActive: status === "failed",
          badge: failed || undefined,
        },
      ],
    },
    {
      title: "Infrastructure",
      link: <a href={to("/infrastructure")} />,
      icon: <ServerIcon />,
      isActive: route.startsWith("/infrastructure"),
      items: [
        { title: "Clouds", onSelect: scrollTo("clouds") },
        { title: "Spend", onSelect: scrollTo("spend") },
        { title: "Resources", onSelect: scrollTo("resources") },
      ],
    },
    {
      title: "Settings",
      icon: <Settings2Icon />,
      items: [
        { title: "Members", onSelect: notMocked("Members") },
        { title: "Secrets", onSelect: notMocked("Secrets") },
        { title: "API tokens", onSelect: notMocked("API tokens") },
        { title: "Audit log", onSelect: notMocked("The audit log") },
      ],
    },
  ]

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader className="gap-3">
        <TeamSwitcher
          activeId="acme"
          onSwitch={(id) =>
            toast(
              `${id === "labs" ? "Acme Labs" : organization.name} is empty in the mock`
            )
          }
          onCreate={() => toast("Creating organizations isn't mocked")}
          orgs={[
            {
              id: "acme",
              name: organization.name,
              detail: `${organization.plan} plan`,
              mark: "brand",
            },
            { id: "labs", name: "Acme Labs", detail: "Hobby plan", mark: "L" },
          ]}
        />
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip="New deployment"
              onClick={() => openDeploy()}
              className="justify-center border bg-background font-medium shadow-xs group-data-[collapsible=icon]:justify-start hover:bg-background hover:shadow-sm"
            >
              <PlusIcon />
              <span>New deployment</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={nav} />
        <NavProjects
          projects={projects.map((p) => {
            const latest = deployments.find(
              (d) => d.project === p.id && d.env === "production"
            )
            const moving = deployments.find(
              (d) => d.project === p.id && isLive(d)
            )
            return {
              id: p.id,
              name: p.name,
              icon: p.icon,
              color: p.color,
              link: <a href={to(`/projects/${p.id}`)} />,
              isActive: route.startsWith(`/projects/${p.id}`),
              attention: attentionFor(moving?.status ?? latest?.status),
            }
          })}
          menu={(p) => {
            const repo = projects.find((x) => x.id === p.id)?.repo ?? p.id
            return (
              <>
                <DropdownMenuItem render={<a href={to(`/projects/${p.id}`)} />}>
                  <FolderGit2Icon />
                  <span>Open project</span>
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => openDeploy({ project: p.id })}>
                  <RocketIcon />
                  <span>Deploy</span>
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => {
                    void navigator.clipboard?.writeText(
                      `loomplane deploy ${p.id}`
                    )
                    toast.success("Copied the CLI command")
                  }}
                >
                  <CopyIcon />
                  <span>Copy deploy command</span>
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => {
                    void navigator.clipboard?.writeText(
                      `https://github.com/${repo}`
                    )
                    toast.success("Copied the repository URL")
                  }}
                >
                  <CopyIcon />
                  <span>Copy repository URL</span>
                </DropdownMenuItem>
              </>
            )
          }}
          onNew={openNewProject}
        />
      </SidebarContent>
      <SidebarFooter>
        <NavUser user={me} onSignOut={() => toast("Signing out isn't mocked")}>
          <DropdownMenuGroup>
            <DropdownMenuItem>
              <UserIcon />
              Account
            </DropdownMenuItem>
            <DropdownMenuItem>
              <KeyRoundIcon />
              API tokens
            </DropdownMenuItem>
            <DropdownMenuItem>
              <BellIcon />
              Notifications
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem onClick={openShortcuts}>
              <KeyboardIcon />
              Keyboard shortcuts
              <DropdownMenuShortcut>?</DropdownMenuShortcut>
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard?.writeText(
                  "curl -fsSL https://loomplane.dev/install.sh | sh"
                )
                toast.success("Copied the CLI install command")
              }}
            >
              <TerminalIcon />
              Install the CLI
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </NavUser>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
