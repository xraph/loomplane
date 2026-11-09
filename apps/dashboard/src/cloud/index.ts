import { createElement, type ReactElement } from "react"
import { CreditCardIcon } from "lucide-react"

import type { SettingsGroup } from "@/components/sidebar/nav-settings"

import { platformAccess } from "./client"
import { platformBillingLink, workspaceBillingLink } from "./links"

// Only the cloud edition imports this module. The public bundle must not
// contain this string; scripts/check-oss-bundle.mjs looks for it.
export const CLOUD_MARKER = "__LOOMPLANE_CLOUD__"

/** What the sidebar knows that the cloud groups depend on. */
export type CloudSettingsContext = {
  at: (path: string) => boolean
  can: (permission: string) => boolean
}

// Whether the signed-in person is the platform owner, asked of the server
// once. The public session says nothing about it, so the sidebar learns it
// here and asks to be drawn again.
let owner = false
let probed = false
const listeners = new Set<() => void>()

function probe() {
  if (probed) return
  probed = true
  platformAccess().then(
    (yes) => {
      if (yes === owner) return
      owner = yes
      listeners.forEach((l) => l())
    },
    () => {
      // Couldn't ask. Ask again the next time the sidebar draws.
      probed = false
    }
  )
}

/** onCloudChange calls cb when what cloudSettingsGroups answers has changed. */
export function onCloudChange(cb: () => void): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

/** forgetOwner is for tests: it makes the next call ask the server again. */
export function forgetOwner(): void {
  owner = false
  probed = false
}

const billingIcon = createElement(CreditCardIcon)

// Sidebar settings groups the cloud edition adds. It stamps the marker on
// <html> so the marker is real code that survives tree-shaking into the cloud
// build's JS, where the bundle guard can find it.
export function cloudSettingsGroups(
  ctx: CloudSettingsContext
): SettingsGroup[] {
  document.documentElement.dataset.edition = CLOUD_MARKER
  probe()
  const item = (link: ReactElement, at: string) => ({
    title: "Billing",
    icon: billingIcon,
    link,
    isActive: ctx.at(at),
  })
  const workspace = item(workspaceBillingLink, "/settings/billing")
  const platform = item(platformBillingLink, "/platform/billing")
  return [
    {
      label: "Workspace",
      items: ctx.can("member:manage") ? [workspace] : [],
    },
    {
      label: "Platform",
      items: owner ? [platform] : [],
    },
  ]
}
