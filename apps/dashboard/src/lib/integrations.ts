// What the Integrations page shows, worked out from what the API returns:
// the repositories each GitHub account lets Loomplane read, and how the
// organization's clouds are doing.
import type {
  CloudForm,
  ConnectedRepository,
  Connection,
  GitHubInstallation,
} from "@/client/forge/loomplane/types"
import { cloudLabel } from "@/lib/clouds"

export type RepositoryGroup = {
  /** The installation's ID, or the owner's name for one that isn't listed. */
  key: string
  account: string
  avatarUrl?: string
  repositories: ConnectedRepository[]
}

/**
 * groupRepositories puts repositories under the account whose installation
 * reaches them, in account order and then by name, keeping only those
 * whose name has query in it. A group left empty isn't shown.
 */
export function groupRepositories(
  repositories: ConnectedRepository[],
  installations: GitHubInstallation[],
  query = ""
): RepositoryGroup[] {
  const q = query.trim().toLowerCase()
  const groups = new Map<string, RepositoryGroup>()
  for (const inst of installations) {
    groups.set(inst.id, {
      key: inst.id,
      account: inst.account,
      avatarUrl: inst.avatarUrl,
      repositories: [],
    })
  }
  for (const r of repositories) {
    if (q && !r.fullName.toLowerCase().includes(q)) continue
    let group = groups.get(r.installation)
    if (!group) {
      // An installation disconnected since the list was fetched.
      const owner = r.fullName.split("/")[0] ?? r.fullName
      group = groups.get(owner) ?? {
        key: owner,
        account: owner,
        repositories: [],
      }
      groups.set(owner, group)
    }
    group.repositories.push(r)
  }
  return [...groups.values()]
    .filter((g) => g.repositories.length > 0)
    .map((g) => ({
      ...g,
      repositories: [...g.repositories].sort((a, b) =>
        a.fullName.localeCompare(b.fullName)
      ),
    }))
    .sort((a, b) => a.account.localeCompare(b.account))
}

export type CloudHealth = {
  ready: number
  /** Connections that are missing permissions or were refused. */
  attention: number
  total: number
}

export function cloudHealth(connections: Connection[]): CloudHealth {
  const ready = connections.filter((c) => c.status === "ready").length
  return {
    ready,
    attention: connections.length - ready,
    total: connections.length,
  }
}

/** What Loomplane runs or provisions on each built-in cloud. */
export const cloudPurposes: Record<string, string> = {
  gcp: "Cloud Run, Cloud SQL, Memorystore and Cloud Storage",
  aws: "ECS on Fargate, RDS, ElastiCache and S3",
  vercel: "Web apps and static sites",
  render: "Web services, workers, Postgres and Key Value",
  digitalocean: "App Platform, Managed PostgreSQL, MySQL and Valkey",
}

// The built-in clouds, for while the API's list is on its way.
const builtinClouds = ["gcp", "aws", "vercel", "render", "digitalocean"]

/**
 * cloudCatalogue is a tile for each cloud a connection can be made to, in
 * the API's order, then one for any cloud a connection is on that the API
 * no longer lists, such as a removed plugin's, so it can still be removed.
 */
export function cloudCatalogue(
  forms: CloudForm[] | undefined,
  connections: Connection[]
): { cloud: string; title: string }[] {
  const out = (
    forms ?? builtinClouds.map((cloud) => ({ cloud, title: "" }))
  ).map((f) => ({ cloud: f.cloud, title: f.title || cloudLabel(f.cloud) }))
  for (const c of connections) {
    if (!out.some((t) => t.cloud === c.cloud)) {
      out.push({ cloud: c.cloud, title: cloudLabel(c.cloud) })
    }
  }
  return out
}

/** A repository a repository field offers, and whether the form has it. */
export type RepositoryChoice = {
  repository: ConnectedRepository
  added: boolean
}

export type ChoiceGroup = {
  key: string
  account: string
  avatarUrl?: string
  items: RepositoryChoice[]
}

/**
 * repositoryChoices is what a repository field offers: the connected
 * repositories, grouped by account, whose names have what's typed in them,
 * each marked added when the form already has it elsewhere. Once the field
 * holds one of them exactly, all of them show again, so another can take
 * its place.
 */
export function repositoryChoices(
  repositories: ConnectedRepository[],
  installations: GitHubInstallation[],
  typed: string,
  exclude: string[] = []
): ChoiceGroup[] {
  const t = typed.trim().toLowerCase()
  const exact = repositories.some((r) => r.fullName.toLowerCase() === t)
  const added = new Set(
    exclude.map((e) => e.trim().toLowerCase()).filter(Boolean)
  )
  return groupRepositories(repositories, installations, exact ? "" : t).map(
    (g) => ({
      key: g.key,
      account: g.account,
      avatarUrl: g.avatarUrl,
      items: g.repositories.map((r) => ({
        repository: r,
        added: added.has(r.fullName.toLowerCase()),
      })),
    })
  )
}
