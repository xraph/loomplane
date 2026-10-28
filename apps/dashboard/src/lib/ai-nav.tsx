// The sidebar's AI group. The pages that make it up add themselves here as
// they ship.
import { AISettingsIcon } from "@/components/icons"
import type { NavItem } from "@/components/sidebar/nav-main"
import { Link } from "@/lib/router"

export type AINavContext = {
  path: string
  can: (permission: string) => boolean
}

/** aiNavItems lists the AI pages someone with ai:read can open. */
export function aiNavItems(ctx: AINavContext): NavItem[] {
  return [
    {
      title: "Settings",
      icon: <AISettingsIcon />,
      link: <Link to="/ai/settings" />,
      isActive: ctx.path.startsWith("/ai/settings"),
    },
  ]
}
