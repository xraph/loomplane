// From shadcn's sidebar-07 block (nav-user.tsx): who's signed in, with an
// account menu. The caller fills the middle of the menu; signing out is
// always last.
import { ChevronsUpDownIcon, LogOutIcon } from "lucide-react"
import type { ReactNode } from "react"

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
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

export type SidebarUser = {
  name: string
  email: string
  initials: string
  image?: string
}

export function NavUser({
  user,
  onSignOut,
  children,
}: {
  user: SidebarUser
  onSignOut: () => void
  /** Menu groups between the account header and signing out. */
  children?: ReactNode
}) {
  const { isMobile } = useSidebar()
  const avatar = (
    <Avatar className="size-8 rounded-lg">
      {user.image && <AvatarImage src={user.image} alt="" />}
      <AvatarFallback className="rounded-lg text-xs font-medium">
        {user.initials}
      </AvatarFallback>
    </Avatar>
  )
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                className="border bg-background shadow-xs group-data-[collapsible=icon]:border-0 group-data-[collapsible=icon]:shadow-none aria-expanded:bg-muted"
              />
            }
          >
            {avatar}
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{user.name}</span>
              <span className="truncate text-xs text-muted-foreground">
                {user.email}
              </span>
            </div>
            <ChevronsUpDownIcon className="ml-auto size-4 text-muted-foreground" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-60"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="p-0 font-normal">
                <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                  {avatar}
                  <div className="grid flex-1 text-left text-sm leading-tight">
                    <span className="truncate font-medium">{user.name}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {user.email}
                    </span>
                  </div>
                </div>
              </DropdownMenuLabel>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            {children}
            {children && <DropdownMenuSeparator />}
            <DropdownMenuItem onClick={onSignOut}>
              <LogOutIcon />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
