import { useEffect, useState } from "react"

import type { SettingsGroup } from "@/components/sidebar/nav-settings"

// The sidebar groups the cloud edition adds, empty until they load and always
// empty in the public edition. The module is loaded with a dynamic import
// inside the edition comparison, so Vite removes the whole branch, and any
// side effect in src/cloud, from a public build.
export function useCloudSettingsGroups(): SettingsGroup[] {
  const [groups, setGroups] = useState<SettingsGroup[]>([])
  useEffect(() => {
    if (import.meta.env.VITE_LOOMPLANE_EDITION === "cloud") {
      void import("@/cloud").then((m) => setGroups(m.cloudSettingsGroups()))
    }
  }, [])
  return groups
}
