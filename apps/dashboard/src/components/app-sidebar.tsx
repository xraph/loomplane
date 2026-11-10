// The dashboard's sidebar, from shadcn's sidebar-07 block: organizations,
// navigation, every project with its icon and color, and the account menu.
// It collapses to icons (⌘B) and the projects stay visible when it does.
import { useAuth } from "@authsome/ui-react"
import { useRouterState } from "@tanstack/react-router"
import {
  CopyIcon,
  FolderGit2Icon,
  KeyboardIcon,
  UserRoundIcon,
} from "lucide-react"
import { type ComponentProps } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlatformCloudHostsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import {
  ActivityIcon,
  AISettingsIcon,
  ApiKeysIcon,
  CloudSignInIcon,
  CloudsIcon,
  DeploymentsIcon,
  DomainsIcon,
  InfrastructureIcon,
  IntegrationsIcon,
  MembersIcon,
  NodesIcon,
  NotificationsIcon,
  OverviewIcon,
  PlatformCloudIcon,
  ProjectsIcon,
  ServicesIcon,
  TeamsIcon,
  VariablesIcon,
  WebhooksIcon,
} from "@/components/icons"
import { AgentOrb } from "@/components/assistant/agent-orb"
import { useAgentDock, useShell } from "@/components/shell"
import { NavMain, type NavItem } from "@/components/sidebar/nav-main"
import { type Attention, NavProjects } from "@/components/sidebar/nav-projects"
import {
  mergeSettingsGroups,
  NavSettings,
  type SettingsGroup,
} from "@/components/sidebar/nav-settings"
import { NavUser } from "@/components/sidebar/nav-user"
import { TeamSwitcher } from "@/components/sidebar/team-switcher"
import { aiNavItems } from "@/lib/ai-nav"
import { useCloudSettingsGroups } from "@/hooks/use-cloud-settings-groups"
import { useAgent, useFeatures } from "@/lib/features"
import { isReady } from "@/lib/projects"
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
import { useInstance } from "@/lib/instance"
import { expiringHosts } from "@/lib/platform-cloud"
import { useOrganization } from "@/lib/organization"
import { anchors } from "@/learn/content"
import { projectStyle } from "@/lib/project-style"
import { Link, useNavigate, usePath } from "@/lib/router"
import { isLive, type Run } from "@/lib/runs"
import { useRuns } from "@/lib/use-runs"

const agentPath = "/agent/requests"
const reportsPath = "/agent/reports"

export function AppSidebar(props: ComponentProps<typeof Sidebar>) {
  const path = usePath()
  const { can } = useOrganization()
  const { platformAdmin } = useInstance()
  const { openNewProject } = useShell()
  const features = useFeatures()
  const agent = useAgent()
  const dock = useAgentDock()
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
    // The agent, for whoever may talk to it: its panel, from any page.
    ...(dock
      ? [
          {
            title: "Assistant",
            icon: <AgentOrb size={16} state={dock.activity} />,
            link: (
              <button
                type="button"
                aria-pressed={dock.open}
                onClick={dock.toggle}
              />
            ),
            isActive: dock.open,
          },
        ]
      : []),
    {
      title: "Deployments",
      icon: <DeploymentsIcon />,
      isActive:
        path.startsWith("/deployments") ||
        path.startsWith(agentPath) ||
        path.startsWith(reportsPath),
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
        // The agent's requests, only where the agent is on for this person.
        ...(features.ai && agent.enabled && can("agent:use")
          ? [
              {
                title: "Agent",
                link: <Link to="/agent/requests" />,
                isActive: path.startsWith(agentPath),
                badge: agent.pending > 0 && (
                  <span className="text-weld-ink">{agent.pending}</span>
                ),
              },
              {
                title: "Reports",
                link: <Link to="/agent/reports" />,
                isActive: path.startsWith(reportsPath),
                badge: agent.reports > 0 && (
                  <span className="text-weld-ink">{agent.reports}</span>
                ),
              },
            ]
          : []),
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
      isActive: path.startsWith("/services"),
    },
    ...(can("organization_cloud:manage")
      ? [
          {
            title: "Domains",
            icon: <DomainsIcon />,
            link: <Link to="/domains" />,
            isActive: path.startsWith("/domains"),
          },
        ]
      : []),
    {
      title: "Clouds",
      icon: <CloudsIcon />,
      link: <Link to="/clouds" />,
      isActive: path.startsWith("/clouds"),
      anchor: anchors.clouds,
    },
    {
      title: "Nodes",
      icon: <NodesIcon />,
      link: <Link to="/nodes" />,
      isActive: path.startsWith("/nodes"),
    },
    {
      title: "Activity",
      icon: <ActivityIcon />,
      link: <Link to="/activity" />,
      isActive: path.startsWith("/activity"),
    },
  ]

  // Settings sit at the foot of the sidebar, apart from the pages you work
  // in, and open beside it rather than pushing the projects up.
  const at = (p: string) => path.startsWith(p)
  const cloudGroups = useCloudSettingsGroups({ at, can })
  const settings: SettingsGroup[] = [
    {
      label: "Workspace",
      items: [
        {
          title: "Members",
          icon: <MembersIcon />,
          link: <Link to="/members" />,
          isActive: at("/members"),
        },
        {
          title: "Teams",
          icon: <TeamsIcon />,
          link: <Link to="/teams" />,
          isActive: at("/teams"),
        },
        {
          title: "Variables",
          icon: <VariablesIcon />,
          link: <Link to="/variables" />,
          isActive: at("/variables"),
        },
        {
          title: "Notifications",
          icon: <NotificationsIcon />,
          link: <Link to="/notifications/settings" />,
          isActive: at("/notifications/settings"),
        },
        ...(can("member:manage")
          ? [
              {
                title: "Webhooks",
                icon: <WebhooksIcon />,
                link: <Link to="/webhooks" />,
                isActive: at("/webhooks"),
              },
            ]
          : []),
      ],
    },
    {
      label: "Connections",
      items: [
        {
          title: "Integrations",
          icon: <IntegrationsIcon />,
          link: <Link to="/integrations" />,
          isActive: at("/integrations"),
        },
        // What runs on the connected clouds, across every project.
        {
          title: "Infrastructure",
          icon: <InfrastructureIcon />,
          link: <Link to="/infrastructure" />,
          isActive: at("/infrastructure"),
        },
        // The workspace's AI settings sit with its other settings, so the AI
        // group above doesn't carry a second "Settings".
        ...(features.ai && can("ai:read")
          ? [
              {
                title: "AI",
                icon: <AISettingsIcon />,
                link: <Link to="/settings/ai" />,
                isActive: at("/settings/ai"),
              },
            ]
          : []),
        {
          title: "API keys",
          icon: <ApiKeysIcon />,
          link: <Link to="/settings/api-keys" />,
          isActive: at("/settings/api-keys"),
        },
      ],
    },
    {
      label: "Platform",
      items: platformAdmin
        ? [
            {
              title: "Cloud sign-in",
              icon: <CloudSignInIcon />,
              link: <Link to="/platform/cloud-sign-in" />,
              isActive: at("/platform/cloud-sign-in"),
            },
            {
              title: "Platform Cloud",
              icon: <PlatformCloudIcon />,
              link: <Link to="/platform/cloud" />,
              isActive: path === "/platform/cloud" || at("/platform/cloud/"),
              badge: <ExpiringCertificatesBadge />,
            },
          ]
        : [],
    },
  ]

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader className="gap-3 pt-3">
        <Organizations />
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={nav} anchor={anchors.nav} />
        {features.ai && can("ai:read") && (
          <NavMain items={aiNavItems({ path, can })} label="AI" />
        )}
        <Projects
          path={path}
          runs={runs}
          onNew={can("project:create") ? openNewProject : undefined}
        />
        <NavSettings
          groups={mergeSettingsGroups(settings, cloudGroups)}
          className="mt-auto"
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
  const navigate = useNavigate()
  const switchTo = (orgId: string) =>
    switchOrganization(orgId).catch((err: unknown) =>
      toast.error(messageOf(err, "Couldn't switch workspace."))
    )
  return (
    <TeamSwitcher
      activeId={session.orgId}
      onSwitch={(id) => void switchTo(id)}
      onCreate={() => void navigate({ to: "/workspaces/new" })}
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
  if (latest.status === "paused") {
    return { tone: "waiting", note: "paused for capacity" }
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
      anchor={anchors.projects}
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
        <DropdownMenuItem render={<Link to="/account/profile" />}>
          <UserRoundIcon />
          Your account
        </DropdownMenuItem>
        <DropdownMenuItem onClick={openShortcuts}>
          <KeyboardIcon />
          Keyboard shortcuts
          <DropdownMenuShortcut>?</DropdownMenuShortcut>
        </DropdownMenuItem>
      </DropdownMenuGroup>
    </NavUser>
  )
}

/**
 * ExpiringCertificatesBadge counts the platform hosts whose client
 * certificate needs rotating. It is mounted only for a platform user, the
 * only one the hosts list answers.
 */
function ExpiringCertificatesBadge() {
  const hosts = useQuery(usePlatformCloudHostsList)
  const n = expiringHosts(hosts.data?.hosts)
  if (n === 0) return null
  return (
    <span
      className="text-destructive"
      title={`${n} host ${n === 1 ? "certificate needs" : "certificates need"} rotating`}
    >
      {n}
    </span>
  )
}
