// From shadcn's sidebar-07 block (nav-projects.tsx): the projects list with
// a hover menu. Each project shows its own icon and color, and a dot on the
// tile when something about it needs a look: work moving, someone's
// approval, or a failure. The list stays when the sidebar collapses to
// icons, so projects are one click away.
import { FolderPlusIcon, MoreHorizontalIcon } from "lucide-react"
import type { ReactElement, ReactNode } from "react"

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"
import { ProjectIcon } from "@/lib/project-style"
import { cn } from "@/lib/utils"

/** Attention is what the dot on a project's tile says, if anything. */
export type Attention = {
  tone: "moving" | "waiting" | "failed"
  /** Read after the name in the tooltip, such as "deploying". */
  note: string
}

export type NavProject = {
  id: string
  name: string
  link: ReactElement
  isActive: boolean
  icon: string
  color: string
  attention?: Attention
}

export function NavProjects({
  projects,
  menu,
  onNew,
}: {
  projects: NavProject[]
  /** The items of a project's hover menu; no menu when omitted. */
  menu?: (p: NavProject) => ReactNode
  /** Shows a New project entry under the list when set. */
  onNew?: () => void
}) {
  const { isMobile } = useSidebar()
  return (
    <SidebarGroup>
      <SidebarGroupLabel>Projects</SidebarGroupLabel>
      <SidebarMenu>
        {projects.map((p) => {
          const note = p.attention?.note
          return (
            <SidebarMenuItem key={p.id}>
              <SidebarMenuButton
                isActive={p.isActive}
                tooltip={note ? `${p.name}, ${note}` : p.name}
                render={p.link}
              >
                {/* Negative margin keeps the tile inside the 16px slot a
                    collapsed button has, while drawing it at 20px. */}
                <span className="relative -m-0.5 flex">
                  <ProjectIcon icon={p.icon} color={p.color} size="xs" />
                  {p.attention && (
                    <span
                      aria-hidden="true"
                      className={cn(
                        "absolute -right-0.5 -bottom-0.5 size-2 rounded-full ring-2 ring-sidebar",
                        p.attention.tone === "moving" &&
                          "animate-pulse bg-weld motion-reduce:animate-none",
                        p.attention.tone === "waiting" &&
                          "bg-sidebar shadow-[inset_0_0_0_1.5px_var(--weld)]",
                        p.attention.tone === "failed" && "bg-destructive"
                      )}
                    />
                  )}
                </span>
                <span className="truncate">{p.name}</span>
                {note && <span className="sr-only">, {note}</span>}
              </SidebarMenuButton>
              {menu && (
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <SidebarMenuAction
                        showOnHover
                        className="aria-expanded:bg-muted"
                      />
                    }
                  >
                    <MoreHorizontalIcon />
                    <span className="sr-only">More for {p.name}</span>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent
                    className="w-52"
                    side={isMobile ? "bottom" : "right"}
                    align={isMobile ? "end" : "start"}
                  >
                    {menu(p)}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </SidebarMenuItem>
          )
        })}
        {onNew && (
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip="New project"
              onClick={onNew}
              className="text-sidebar-foreground/70"
            >
              <span className="-m-0.5 flex size-5 items-center justify-center rounded-[5px] border border-dashed border-sidebar-foreground/30">
                <FolderPlusIcon className="size-3!" />
              </span>
              <span>New project</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        )}
      </SidebarMenu>
    </SidebarGroup>
  )
}
