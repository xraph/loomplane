// The dashboard's sidebar, from shadcn's sidebar-07 block: organizations,
// navigation, every project with its icon and color, and the account menu.
// It collapses to icons (⌘B) and the projects stay visible when it does.
import { useAuth } from "@authsome/ui-react"
import { useRouterState } from "@tanstack/react-router"
import {
  CopyIcon,
  FolderGit2Icon,
  KeyboardIcon,
  Settings2Icon,
} from "lucide-react"
import { type ComponentProps, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import {
  ActivityIcon,
  CloudsIcon,
  DeploymentsIcon,
  InfrastructureIcon,
  OverviewIcon,
  ProjectsIcon,
  ServicesIcon,
} from "@/components/icons"
import { OrganizationForm } from "@/components/organization-form"
import { useShell } from "@/components/shell"
import { NavMain, type NavItem } from "@/components/sidebar/nav-main"
import { type Attention, NavProjects } from "@/components/sidebar/nav-projects"
import { NavUser } from "@/components/sidebar/nav-user"
import { TeamSwitcher } from "@/components/sidebar/team-switcher"
import { isReady } from "@/lib/projects"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuShortcut,
} from "@/components/ui/dropdown-menu"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarRail,
} from "@/components/ui/sidebar"
import { useOrganization } from "@/lib/organization"
import { projectStyle } from "@/lib/project-style"
import { Link, usePath } from "@/lib/router"
import { isLive, type Run } from "@/lib/runs"
import { useRuns } from "@/lib/use-runs"

const settingsPaths = [
  "/members",
  "/variables",
  "/integrations",
  "/cloud-sign-in",
  "/api-keys",
  "/notifications",
]

export function AppSidebar(props: ComponentProps<typeof Sidebar>) {
  const path = usePath()
  const { can } = useOrganization()
  const { openNewProject } = useShell()
  const { runs } = useRuns()
  const status = useRouterState({
    select: (s) =>
      s.location.pathname === "/deployments"
        ? ((s.location.search as { status?: string }).status ?? "all")
        : "",
  })
  const live = runs.filter(isLive).length
  const waiting = runs.filter((r) => r.status === "waiting").length
  const failed = runs.filter(
    (r) => r.status === "failed" || r.status === "blocked"
  ).length

  const nav: NavItem[] = [
    {
      title: "Overview",
      icon: <OverviewIcon />,
      link: <Link to="/" />,
      isActive: path === "/",
    },
    {
      title: "Deployments",
      icon: <DeploymentsIcon />,
      isActive: path.startsWith("/deployments"),
      badge: waiting > 0 && (
        <span className="rounded-full bg-weld-tint px-1.5 font-mono text-[11px] text-weld-ink tabular-nums">
          {waiting}
        </span>
      ),
      items: [
        {
          title: "All deployments",
          link: <Link to="/deployments" />,
          isActive: status === "all",
        },
        {
          title: "In flight",
          link: <Link to="/deployments" search={{ status: "live" }} />,
          isActive: status === "live",
          badge: live > 0 && <span className="text-flight-ink">{live}</span>,
        },
        {
          title: "Needs approval",
          link: <Link to="/deployments" search={{ status: "waiting" }} />,
          isActive: status === "waiting",
          badge: waiting > 0 && (
            <span className="text-weld-ink">{waiting}</span>
          ),
        },
        {
          title: "Failed",
          link: <Link to="/deployments" search={{ status: "failed" }} />,
          isActive: status === "failed",
          badge: failed > 0 && (
            <span className="text-destructive">{failed}</span>
          ),
        },
      ],
    },
    {
      title: "Projects",
      icon: <ProjectsIcon />,
      link: <Link to="/projects" />,
      isActive: path === "/projects",
    },
    {
      title: "Services",
      icon: <ServicesIcon />,
      link: <Link to="/services" />,
      isActive: path === "/services",
    },
    {
      title: "Clouds",
      icon: <CloudsIcon />,
      link: <Link to="/clouds" />,
      isActive: path.startsWith("/clouds"),
    },
    {
      title: "Infrastructure",
      icon: <InfrastructureIcon />,
      link: <Link to="/infrastructure" />,
      isActive: path.startsWith("/infrastructure"),
    },
    {
      title: "Activity",
      icon: <ActivityIcon />,
      link: <Link to="/activity" />,
      isActive: path.startsWith("/activity"),
    },
    {
      title: "Settings",
      icon: <Settings2Icon />,
      isActive: settingsPaths.some((p) => path.startsWith(p)),
      items: [
        {
          title: "Notifications",
          link: <Link to="/notifications" />,
          isActive: path.startsWith("/notifications"),
        },
        {
          title: "Members",
          link: <Link to="/members" />,
          isActive: path.startsWith("/members"),
        },
        {
          title: "Variables",
          link: <Link to="/variables" />,
          isActive: path.startsWith("/variables"),
        },
        {
          title: "Integrations",
          link: <Link to="/integrations" />,
          isActive: path.startsWith("/integrations"),
        },
        {
          title: "Cloud sign-in",
          link: <Link to="/cloud-sign-in" />,
          isActive: path.startsWith("/cloud-sign-in"),
        },
        {
          title: "API keys",
          link: <Link to="/api-keys" />,
          isActive: path.startsWith("/api-keys"),
        },
      ],
    },
  ]

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader className="gap-3 pt-3">
        <Organizations />
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={nav} />
        <Projects
          path={path}
          runs={runs}
          onNew={can("project:create") ? openNewProject : undefined}
        />
      </SidebarContent>
      <SidebarFooter className="border-t border-sidebar-border">
        <Account />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}

function Organizations() {
  const { session, organizations, switchOrganization } = useOrganization()
  const [creating, setCreating] = useState(false)
  const switchTo = (orgId: string) =>
    switchOrganization(orgId).catch((err: unknown) =>
      toast.error(messageOf(err, "Couldn't switch organization."))
    )
  return (
    <>
      <TeamSwitcher
        activeId={session.orgId}
        onSwitch={(id) => void switchTo(id)}
        onCreate={() => setCreating(true)}
        orgs={organizations.map((o) => ({
          id: o.id,
          name: o.name,
          detail: o.slug,
          // The active organization wears Loomplane's mark, so the corner
          // still says where you are; the others go by their initial.
          mark:
            o.id === session.orgId
              ? "brand"
              : (o.name.trim()[0]?.toUpperCase() ?? "?"),
        }))}
      />
      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>New organization</DialogTitle>
            <DialogDescription>
              A separate home for another team&apos;s projects. You&apos;ll be
              its owner.
            </DialogDescription>
          </DialogHeader>
          <OrganizationForm
            onCreated={async (orgId) => {
              await switchOrganization(orgId)
              setCreating(false)
            }}
          />
        </DialogContent>
      </Dialog>
    </>
  )
}

// The dot on a project's tile: only for what needs a look.
function attentionFor(latest?: Run): Attention | undefined {
  if (!latest) return undefined
  if (latest.status === "running") {
    return {
      tone: "moving",
      note: latest.deployment ? "deploying" : "planning",
    }
  }
  if (latest.status === "waiting") {
    return { tone: "waiting", note: "waiting for approval" }
  }
  if (latest.status === "failed") {
    return { tone: "failed", note: "last deploy failed" }
  }
  if (latest.status === "blocked") {
    return { tone: "failed", note: "plan blocked" }
  }
  return undefined
}

function Projects({
  path,
  runs,
  onNew,
}: {
  path: string
  runs: Run[]
  onNew?: () => void
}) {
  const list = useQuery(useProjectsList)
  if (!list.data) {
    return (
      <SidebarGroup>
        <SidebarGroupLabel>Projects</SidebarGroupLabel>
        <SidebarMenu>
          {[0, 1, 2].map((i) => (
            <SidebarMenuItem key={i}>
              <SidebarMenuSkeleton showIcon />
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroup>
    )
  }
  return (
    <NavProjects
      projects={list.data.projects.map((p) => {
        const style = projectStyle(p)
        const attention: Attention | undefined = isReady(p.status)
          ? attentionFor(runs.find((r) => r.projectId === p.id))
          : { tone: "moving", note: "setting up" }
        return {
          id: p.id,
          name: p.name,
          icon: style.icon,
          color: style.color,
          link: <Link to="/projects/$id" params={{ id: p.id }} />,
          isActive: path === `/projects/${p.id}`,
          attention,
        }
      })}
      menu={(p) => {
        const project = list.data?.projects.find((x) => x.id === p.id)
        const repository = project?.repositories[0]
        return (
          <>
            <DropdownMenuItem
              render={<Link to="/projects/$id" params={{ id: p.id }} />}
            >
              <FolderGit2Icon />
              <span>Open project</span>
            </DropdownMenuItem>
            {repository && (
              <DropdownMenuItem
                onClick={() => {
                  void navigator.clipboard?.writeText(
                    `https://github.com/${repository}`
                  )
                  toast.success("Copied the repository URL")
                }}
              >
                <CopyIcon />
                <span>Copy repository URL</span>
              </DropdownMenuItem>
            )}
            <DropdownMenuItem
              onClick={() => {
                void navigator.clipboard?.writeText(p.id)
                toast.success("Copied the project ID")
              }}
            >
              <CopyIcon />
              <span>Copy project ID</span>
            </DropdownMenuItem>
          </>
        )
      }}
      onNew={onNew}
    />
  )
}

function Account() {
  const { user, signOut } = useAuth()
  const { openShortcuts } = useShell()
  if (!user) return null
  const full = `${user.first_name ?? ""} ${user.last_name ?? ""}`.trim()
  const name = full || user.display_username || user.email
  const initials =
    name
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join("") || "?"
  return (
    <NavUser
      user={{ name, email: user.email, initials, image: user.image }}
      onSignOut={() => {
        // A full load, like authsome's own account menu: nothing cached
        // for this organization survives into the next session.
        void signOut().then(() => {
          window.location.href = "/sign-in"
        })
      }}
    >
      <DropdownMenuGroup>
        <DropdownMenuItem onClick={openShortcuts}>
          <KeyboardIcon />
          Keyboard shortcuts
          <DropdownMenuShortcut>?</DropdownMenuShortcut>
        </DropdownMenuItem>
      </DropdownMenuGroup>
    </NavUser>
  )
}
