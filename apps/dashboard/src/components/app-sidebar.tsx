import { OrgSwitcher, UserButton } from "@authsome/ui-components"
import { FolderGit2Icon, PlugIcon, ServerIcon, UsersIcon } from "lucide-react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar"
import { Mark } from "@/components/weave"
import { useOrganization } from "@/lib/organization"
import { Link, usePath } from "@/lib/router"

const nav = [
  {
    to: "/",
    label: "Projects",
    icon: FolderGit2Icon,
    active: (p: string) => p === "/" || p.startsWith("/projects"),
  },
  {
    to: "/infrastructure",
    label: "Infrastructure",
    icon: ServerIcon,
    active: (p: string) => p.startsWith("/infrastructure"),
  },
  {
    to: "/members",
    label: "Members",
    icon: UsersIcon,
    active: (p: string) => p.startsWith("/members"),
  },
  {
    to: "/integrations",
    label: "Integrations",
    icon: PlugIcon,
    active: (p: string) => p.startsWith("/integrations"),
  },
] as const

export function AppSidebar() {
  const path = usePath()
  const { session, switchOrganization } = useOrganization()

  return (
    <Sidebar>
      <SidebarHeader className="gap-3">
        <Link to="/" className="flex items-center gap-2 px-2 pt-1 font-medium">
          <span className="flex size-7 items-center justify-center rounded-md bg-sidebar-primary text-sidebar-primary-foreground">
            <Mark className="size-5" />
          </span>
          Loomplane
        </Link>
        {/* authsome's switcher: lists the user's organizations, creates new
            ones, and reports the choice; Loomplane makes it the session's
            active organization. */}
        <OrgSwitcher
          activeOrgId={session.orgId}
          onOrgChange={(orgId) => {
            switchOrganization(orgId).catch((err) =>
              toast.error(messageOf(err, "Couldn't switch organization."))
            )
          }}
        />
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Platform</SidebarGroupLabel>
          <SidebarMenu>
            {nav.map((n) => (
              <SidebarMenuItem key={n.to}>
                <SidebarMenuButton
                  render={<Link to={n.to} />}
                  isActive={n.active(path)}
                >
                  <n.icon />
                  <span>{n.label}</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        {/* authsome's account menu: profile and sign-out. */}
        <UserButton afterSignOutUrl="/sign-in" />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
