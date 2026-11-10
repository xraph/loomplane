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
  // badge flags something waiting on the page. It shows on the Settings
  // button too, so it is seen with the menu closed. It may render nothing.
  badge?: ReactNode
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
  const badged = shown.flatMap((g) => g.items).filter((i) => i.badge)
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
              {badged.length > 0 && (
                <span className="ml-auto flex gap-1 font-mono text-[11.5px] tabular-nums">
                  {badged.map((i) => (
                    <Fragment key={i.title}>{i.badge}</Fragment>
                  ))}
                </span>
              )}
              <ChevronRightIcon
                className={
                  badged.length > 0
                    ? "text-muted-foreground"
                    : "ml-auto text-muted-foreground"
                }
              />
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
                        {item.badge && (
                          <span className="ml-auto font-mono text-[11.5px] tabular-nums">
                            {item.badge}
                          </span>
                        )}
                        {item.isActive && (
                          <CheckIcon
                            className={
                              item.badge
                                ? "text-muted-foreground"
                                : "ml-auto text-muted-foreground"
                            }
                          />
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
