// The sidebar's AI group. The pages that make it up add themselves here as
// they ship.
import { AISettingsIcon, ModelsIcon } from "@/components/icons"
import type { NavItem } from "@/components/sidebar/nav-main"
import { Link } from "@/lib/router"

export type AINavContext = {
  path: string
  can: (permission: string) => boolean
}

/** aiNavItems lists the AI pages someone with ai:read can open. */
export function aiNavItems(ctx: AINavContext): NavItem[] {
  const items: NavItem[] = []
  if (ctx.can("ai:read"))
    items.push({
      title: "Models",
      icon: <ModelsIcon />,
      link: <Link to="/ai/models" />,
      isActive: ctx.path === "/ai/models" || ctx.path.startsWith("/ai/models/"),
    })
  return [
    ...items,
    {
      title: "Settings",
      icon: <AISettingsIcon />,
      link: <Link to="/ai/settings" />,
      isActive: ctx.path.startsWith("/ai/settings"),
    },
  ]
}
