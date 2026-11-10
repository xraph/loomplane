// From shadcn's sidebar-07 block (nav-main.tsx): collapsible sections with
// sub-pages, plus active states and plain links for sections without
// children. Links are elements the caller renders, so the real dashboard
// passes router Links and the mock passes hash anchors.
import { ChevronRightIcon } from "lucide-react"
import { type ReactElement, type ReactNode, useState } from "react"

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
} from "@/components/ui/sidebar"
import { cn } from "@/lib/utils"

export type NavSubItem = {
  title: string
  /** The link to render; a sub-item without one runs onSelect instead. */
  link?: ReactElement
  onSelect?: () => void
  isActive?: boolean
  badge?: ReactNode
}

export type NavItem = {
  title: string
  icon: ReactNode
  /** The link for a section without sub-items. */
  link?: ReactElement
  isActive?: boolean
  badge?: ReactNode
  items?: NavSubItem[]
  /** A data-tour name, so a walkthrough can point at the entry. */
  anchor?: string
}

export function NavMain({
  items,
  label = "Platform",
  className,
  anchor,
}: {
  items: NavItem[]
  /** The group's heading; leave it empty for a group that needs none. */
  label?: string
  className?: string
  /** A data-tour name for the group, so a walkthrough can point at it. */
  anchor?: string
}) {
  // A section opens while you're in it, unless you've toggled it yourself.
  const [manual, setManual] = useState<Record<string, boolean>>({})
  return (
    <SidebarGroup className={className} data-tour={anchor}>
      {label && <SidebarGroupLabel>{label}</SidebarGroupLabel>}
      <SidebarMenu>
        {items.map((item) =>
          item.items?.length ? (
            <Collapsible
              key={item.title}
              open={manual[item.title] ?? !!item.isActive}
              onOpenChange={(open) =>
                setManual((m) => ({ ...m, [item.title]: open }))
              }
              className="group/collapsible"
              render={<SidebarMenuItem />}
            >
              <CollapsibleTrigger
                render={
                  <SidebarMenuButton
                    tooltip={item.title}
                    isActive={item.isActive}
                  />
                }
              >
                {item.icon}
                <span>{item.title}</span>
                {/* A closed section still says when something in it waits. */}
                {item.badge && (
                  <span className="ml-auto group-data-open/collapsible:hidden">
                    {item.badge}
                  </span>
                )}
                <ChevronRightIcon
                  className={cn(
                    "text-muted-foreground transition-transform duration-200 group-data-open/collapsible:rotate-90",
                    item.badge
                      ? "group-data-open/collapsible:ml-auto"
                      : "ml-auto"
                  )}
                />
              </CollapsibleTrigger>
              <CollapsibleContent>
                <SidebarMenuSub>
                  {item.items.map((sub) => (
                    <SidebarMenuSubItem key={sub.title}>
                      <SidebarMenuSubButton
                        isActive={sub.isActive}
                        render={
                          sub.link ?? (
                            <button type="button" onClick={sub.onSelect} />
                          )
                        }
                      >
                        <span>{sub.title}</span>
                        {sub.badge && (
                          <span className="ml-auto font-mono text-[11.5px] text-muted-foreground tabular-nums">
                            {sub.badge}
                          </span>
                        )}
                      </SidebarMenuSubButton>
                    </SidebarMenuSubItem>
                  ))}
                </SidebarMenuSub>
              </CollapsibleContent>
            </Collapsible>
          ) : (
            <SidebarMenuItem key={item.title} data-tour={item.anchor}>
              <SidebarMenuButton
                tooltip={item.title}
                isActive={item.isActive}
                render={item.link}
              >
                {item.icon}
                <span>{item.title}</span>
              </SidebarMenuButton>
              {item.badge && <SidebarMenuBadge>{item.badge}</SidebarMenuBadge>}
            </SidebarMenuItem>
          )
        )}
      </SidebarMenu>
    </SidebarGroup>
  )
}
