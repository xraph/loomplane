import { useEffect, useState } from "react"

import type { SettingsGroup } from "@/components/sidebar/nav-settings"
import type { CloudSettingsContext } from "@/cloud"

// The sidebar groups the cloud edition adds, empty until they load and always
// empty in the public edition. The module is loaded with a dynamic import
// inside the edition comparison, so Vite removes the whole branch, and any
// side effect in src/cloud, from a public build. The type import is erased.
//
// The module is loaded once and asked again on every render, so the groups
// follow the permissions in ctx. It also says when something it learned
// afterwards changes the groups, and the hook renders again.
type CloudModule = typeof import("@/cloud")

export function useCloudSettingsGroups(
  ctx: CloudSettingsContext
): SettingsGroup[] {
  const [cloud, setCloud] = useState<CloudModule | null>(null)
  const [, bump] = useState(0)
  useEffect(() => {
    if (import.meta.env.VITE_LOOMPLANE_EDITION !== "cloud") return
    let off = () => {}
    let gone = false
    void import("@/cloud").then((m) => {
      if (gone) return
      off = m.onCloudChange(() => bump((n) => n + 1))
      setCloud(m)
    })
    return () => {
      gone = true
      off()
    }
  }, [])
  return cloud ? cloud.cloudSettingsGroups(ctx) : []
}
