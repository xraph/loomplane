// The dashboard's sidebar, from shadcn's sidebar-07 block: organizations,
// navigation, every project with its icon and color, and the account menu.
// It collapses to icons (⌘B) and the projects stay visible when it does.
import { useAuth } from "@authsome/ui-react"
import {
  ActivityIcon,
  CopyIcon,
  FolderGit2Icon,
  KeyboardIcon,
  LayoutGridIcon,
  ServerIcon,
  Settings2Icon,
} from "lucide-react"
import { type ComponentProps, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
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

const settingsPaths = ["/members", "/variables", "/integrations", "/api-keys"]

export function AppSidebar(props: ComponentProps<typeof Sidebar>) {
  const path = usePath()
  const { can } = useOrganization()
  const { openNewProject } = useShell()

  const nav: NavItem[] = [
    {
      title: "Projects",
      icon: <LayoutGridIcon />,
      link: <Link to="/" />,
      isActive: path === "/",
    },
    {
      title: "Infrastructure",
      icon: <ServerIcon />,
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
          title: "API keys",
          link: <Link to="/api-keys" />,
          isActive: path.startsWith("/api-keys"),
        },
      ],
    },
  ]

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader>
        <Organizations />
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={nav} />
        <Projects
          path={path}
          onNew={can("project:create") ? openNewProject : undefined}
        />
      </SidebarContent>
      <SidebarFooter>
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

function Projects({ path, onNew }: { path: string; onNew?: () => void }) {
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
          ? undefined
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
