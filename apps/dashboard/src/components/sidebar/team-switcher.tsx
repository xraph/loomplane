// From shadcn's sidebar-07 block (team-switcher.tsx), with organizations in
// place of the sample teams. The caller owns which one is active and what
// switching does.
import { ChevronsUpDownIcon, PlusIcon } from "lucide-react"

import { BrandMark } from "@/components/brand-mark"
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
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"
import { cn } from "@/lib/utils"

export type SwitcherOrg = {
  id: string
  name: string
  /** The second line under the name, such as the plan or your role. */
  detail?: string
  /** "brand" draws Loomplane's mark; anything else is drawn as text. */
  mark: "brand" | string
}

export function TeamSwitcher({
  orgs,
  activeId,
  onSwitch,
  onCreate,
}: {
  orgs: SwitcherOrg[]
  activeId: string
  onSwitch: (id: string) => void
  onCreate?: () => void
}) {
  const { isMobile } = useSidebar()
  const active = orgs.find((o) => o.id === activeId) ?? orgs[0]
  if (!active) return null
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
              />
            }
          >
            <OrgMark org={active} className="size-8 rounded-lg" />
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-semibold tracking-tight">
                {active.name}
              </span>
              {active.detail && (
                <span className="truncate text-xs text-muted-foreground">
                  {active.detail}
                </span>
              )}
            </div>
            <ChevronsUpDownIcon className="ml-auto text-muted-foreground" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-60"
            align="start"
            side={isMobile ? "bottom" : "right"}
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                Organizations
              </DropdownMenuLabel>
              {orgs.map((org) => (
                <DropdownMenuItem
                  key={org.id}
                  onClick={() => org.id !== active.id && onSwitch(org.id)}
                  className="gap-2 p-2"
                >
                  <OrgMark org={org} className="size-6 rounded-md" />
                  <span className="truncate">{org.name}</span>
                  {org.id === active.id && (
                    <span className="ml-auto size-1.5 rounded-full bg-overdye" />
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
            {onCreate && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuGroup>
                  <DropdownMenuItem className="gap-2 p-2" onClick={onCreate}>
                    <div className="flex size-6 items-center justify-center rounded-md border bg-transparent">
                      <PlusIcon className="size-4" />
                    </div>
                    <div className="font-medium text-muted-foreground">
                      New organization
                    </div>
                  </DropdownMenuItem>
                </DropdownMenuGroup>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}

function OrgMark({ org, className }: { org: SwitcherOrg; className?: string }) {
  return (
    <div
      className={cn(
        "flex aspect-square shrink-0 items-center justify-center border bg-background text-foreground",
        className
      )}
    >
      {org.mark === "brand" ? (
        <BrandMark className="size-[55%]" />
      ) : (
        <span className="text-xs font-semibold">{org.mark}</span>
      )}
    </div>
  )
}
