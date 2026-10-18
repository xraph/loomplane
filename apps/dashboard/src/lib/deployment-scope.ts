import { useState } from "react"
import { useDeployedList, useSourcesList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { chosenScope, lockedOn, scopeBody, toggleScope } from "@/lib/plans"

// Both plan entry points keep undeployed repositories in scope. Source
// aliases stay immutable; held repositories use their deployed lockset.
export function useDeploymentScope(projectId: string, environment: string) {
  const sources = useQuery(useSourcesList, { path: { id: projectId } })
  const deployed = useQuery(useDeployedList, {
    path: { id: projectId, environment },
  })
  const [moving, setMoving] = useState<string[] | null>(null)
  const aliases = (sources.data?.sources ?? [])
    .filter((s) => !s.removedAt)
    .map((s) => s.alias)
  const running = deployed.data?.sources.map((d) => d.alias)
  const known = running !== undefined
  const locked = known ? lockedOn(aliases, running) : new Set(aliases)
  const chosen = chosenScope(aliases, moving, locked)
  return {
    sources,
    deployed,
    aliases,
    known,
    locked,
    chosen,
    body: scopeBody(aliases, chosen),
    toggle: (alias: string) =>
      setMoving(toggleScope(aliases, chosen, alias, locked)),
    ready:
      !!sources.data &&
      !!deployed.data &&
      sources.status !== "error" &&
      deployed.status !== "error" &&
      (aliases.length === 0 || chosen.length > 0),
  }
}
