// Settings at the foot of the sidebar: one button that opens a menu beside
// it, grouped by what each page looks after. The sidebar stays the same
// height whichever page you're on, and the menu works the same when the
// sidebar is folded to icons.
import { CheckIcon, ChevronRightIcon } from "lucide-react"
import { Fragment, type ReactElement, type ReactNode } from "react"

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  SidebarGroup,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"
import { SettingsIcon } from "@/components/icons"

export type SettingsLink = {
  title: string
  icon: ReactNode
  link: ReactElement
  isActive?: boolean
}

export type SettingsGroup = { label: string; items: SettingsLink[] }

/**
 * mergeSettingsGroups adds another edition's groups to the base's. Groups
 * with the same label are merged, the extra items going after the base's;
 * a label the base lacks is added at the end.
 */
export function mergeSettingsGroups(
  base: SettingsGroup[],
  extra: SettingsGroup[]
): SettingsGroup[] {
  const merged = base.map((g) => ({ ...g, items: [...g.items] }))
  for (const group of extra) {
    const same = merged.find((g) => g.label === group.label)
    if (same) same.items.push(...group.items)
    else merged.push({ ...group, items: [...group.items] })
  }
  return merged
}

export function NavSettings({
  groups,
  className,
}: {
  groups: SettingsGroup[]
  className?: string
}) {
  const { isMobile } = useSidebar()
  const shown = groups.filter((g) => g.items.length > 0)
  const active = shown.some((g) => g.items.some((i) => i.isActive))
  return (
    <SidebarGroup className={className}>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <SidebarMenuButton
                  tooltip="Settings"
                  isActive={active}
                  className="aria-expanded:bg-sidebar-accent"
                />
              }
            >
              <SettingsIcon />
              <span>Settings</span>
              <ChevronRightIcon className="ml-auto text-muted-foreground" />
            </DropdownMenuTrigger>
            <DropdownMenuContent
              className="w-60"
              side={isMobile ? "top" : "right"}
              align="end"
              sideOffset={8}
            >
              {shown.map((group, i) => (
                <Fragment key={group.label}>
                  {i > 0 && <DropdownMenuSeparator />}
                  <DropdownMenuGroup>
                    <DropdownMenuLabel>{group.label}</DropdownMenuLabel>
                    {group.items.map((item) => (
                      <DropdownMenuItem
                        key={item.title}
                        render={item.link}
                        aria-current={item.isActive ? "page" : undefined}
                        className="aria-[current=page]:font-medium [&>svg:first-child]:text-muted-foreground"
                      >
                        {item.icon}
                        <span>{item.title}</span>
                        {item.isActive && (
                          <CheckIcon className="ml-auto text-muted-foreground" />
                        )}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuGroup>
                </Fragment>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarGroup>
  )
}
