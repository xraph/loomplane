import type { SettingsGroup } from "@/components/sidebar/nav-settings"

// Only the cloud edition imports this module. The public bundle must not
// contain this string; scripts/check-oss-bundle.mjs looks for it.
export const CLOUD_MARKER = "__LOOMPLANE_CLOUD__"

// Sidebar settings groups the cloud edition adds. None yet. It stamps the
// marker on <html> so the marker is real code that survives tree-shaking into
// the cloud build's JS, where the bundle guard can find it.
export function cloudSettingsGroups(): SettingsGroup[] {
  document.documentElement.dataset.edition = CLOUD_MARKER
  return []
}
