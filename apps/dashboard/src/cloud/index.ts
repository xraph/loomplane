import type { SettingsGroup } from "@/components/sidebar/nav-settings"

// Only the cloud edition imports this module. The public bundle must not
// contain this string; scripts/check-oss-bundle.mjs looks for it.
export const CLOUD_MARKER = "__LOOMPLANE_CLOUD__"

// Sidebar settings groups the cloud edition adds. None yet.
export function cloudSettingsGroups(): SettingsGroup[] {
  return []
}
