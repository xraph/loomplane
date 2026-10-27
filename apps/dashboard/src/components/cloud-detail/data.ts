// What a Cloud page reads, in the viewer's scope. An organization manager
// reads the organization's view; anyone else reads the Cloud through the one
// project it was granted to, and sees only that project's share of it.
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  DefaultPlacement,
  Profile,
  Sync,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { cloudLabel } from "@/lib/clouds"

export type CloudScope = {
  cloud: Profile
  /** The project the Cloud is read through, or "" for the organization. */
  project: string
}

// The scope never changes for a mounted page (the route keys it), so each
// hook picks one binding for its whole life.
export function useScopedTargets({ cloud, project }: CloudScope) {
  const q = useQuery(
    project ? api.useCloudTargetsList : api.useOrganizationCloudTargetsList,
    { path: { id: project, cloudId: cloud.id } }
  )
  return {
    ...q,
    list: (q.data?.targets ?? []).filter((t): t is Target => !!t),
  }
}

export function useScopedUsage({ cloud, project }: CloudScope) {
  return useQuery(
    project ? api.useCloudsUsage : api.useOrganizationCloudsUsage,
    { path: { id: project, cloudId: cloud.id } }
  )
}

export function useScopedSyncs(
  { cloud, project }: CloudScope,
  before = "",
  limit = 30
) {
  const q = useQuery(
    project ? api.useCloudSyncsList : api.useOrganizationCloudSyncsList,
    {
      path: { id: project },
      query: { cloud: cloud.id, limit, ...(before ? { before } : {}) },
    }
  )
  return { ...q, list: (q.data?.syncs ?? []).filter((s): s is Sync => !!s) }
}

/** The key a placement counts under: a target's ID, or the default. */
export const DEFAULT_KEY = "@default"

/** servicesPlaced counts running services by target, and by the default. */
export function servicesPlaced(usage?: Usage): Map<string, number> {
  const out = new Map<string, number>()
  for (const env of usage?.environments ?? [])
    for (const p of env.placements) {
      const key = p.default ? DEFAULT_KEY : (p.targetId ?? "")
      out.set(key, (out.get(key) ?? 0) + p.services.length)
    }
  return out
}

export const serviceCount = (n: number) =>
  `${n} ${n === 1 ? "service" : "services"}`

/** An account's name, or what it ran on when the account is gone. */
export function accountName(connections: Connection[], id?: string) {
  return connections.find((c) => c.id === id)?.name ?? "A removed account"
}

/** placementLabel reads a target or default as "Account · region". */
export function placementLabel(
  connections: Connection[],
  p?:
    | Pick<Target, "runtime" | "poolId" | "connectionId" | "region">
    | DefaultPlacement
    | null
) {
  if (!p) return "No default"
  if ("runtime" in p && p.runtime === "server") return "A server pool"
  return `${accountName(connections, p.connectionId)} · ${p.region || "its default region"}`
}

/** Where a target or default runs, for the line under its account. */
export function whereLabel(
  connections: Connection[],
  p: { provider: string; connectionId: string; region: string }
) {
  const account = connections.find((c) => c.id === p.connectionId)
  return [
    cloudLabel(account?.cloud ?? p.provider),
    account?.account,
    p.region || "default region",
  ]
    .filter(Boolean)
    .join(" · ")
}

/** Syncs still moving something, which the page polls while they run. */
export const movingSync = (s: Sync) =>
  [
    "pending",
    "running",
    "reverting",
    "routing_pending",
    "cleanup_pending",
  ].includes(s.status)
