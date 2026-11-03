// What the Integrations page shows, worked out from what the API returns:
// the repositories each GitHub account and GitLab connection lets Loomplane
// read, and how the organization's clouds are doing.
import type {
  CloudForm,
  ConnectedRepository,
  Connection,
  GitHubInstallation,
  GitLabRepository,
} from "@/client/forge/loomplane/types"
import { cloudLabel } from "@/lib/clouds"

/**
 * A repository a repository field can offer, from whichever host lists it.
 * Its key is what a source stores: owner/name on GitHub, host/path anywhere
 * else.
 */
export type PickerRepository = {
  key: string
  /** The name without the host: owner/name, or the GitLab project's path. */
  fullName: string
  host: string
  provider: "github" | "gitlab"
  private: boolean
  /** What the field groups it under: an installation, or a namespace. */
  group: string
  defaultBranch?: string
}

export function fromGitHub(r: ConnectedRepository): PickerRepository {
  return {
    key: r.fullName,
    fullName: r.fullName,
    host: "github.com",
    provider: "github",
    private: r.private,
    group: r.installation,
    defaultBranch: r.defaultBranch,
  }
}

export function fromGitLab(r: GitLabRepository): PickerRepository {
  return {
    key: r.key,
    fullName: r.fullName,
    host: r.host,
    provider: "gitlab",
    private: r.private,
    group: `gitlab:${r.host}:${r.namespace}`,
    defaultBranch: r.defaultBranch,
  }
}

/** What a group of repositories is headed with. */
export type GroupLabel = { account: string; avatarUrl?: string }

/**
 * pickerGroupLabel names a group: a GitHub account, or a GitLab namespace
 * that says its host when that isn't gitlab.com.
 */
export function pickerGroupLabel(
  installations: GitHubInstallation[]
): (group: string, sample: PickerRepository) => GroupLabel {
  const accounts = new Map(installations.map((i) => [i.id, i]))
  return (group, sample) => {
    if (sample.provider === "gitlab") {
      const prefix = `gitlab:${sample.host}:`
      const namespace = group.startsWith(prefix)
        ? group.slice(prefix.length)
        : sample.fullName.split("/").slice(0, -1).join("/")
      return {
        account:
          sample.host === "gitlab.com"
            ? namespace
            : `${namespace} on ${sample.host}`,
      }
    }
    const inst = accounts.get(group)
    // An installation disconnected since the list was fetched.
    if (!inst) return { account: sample.fullName.split("/")[0] }
    return { account: inst.account, avatarUrl: inst.avatarUrl }
  }
}

export type RepositoryGroup = {
  key: string
  account: string
  avatarUrl?: string
  repositories: PickerRepository[]
}

/** matches is whether a repository's key or name has the lowercase q in it. */
function matches(r: PickerRepository, q: string): boolean {
  return (
    !q ||
    r.key.toLowerCase().includes(q) ||
    r.fullName.toLowerCase().includes(q)
  )
}

/**
 * groupRepositories puts repositories under their group, headed by what
 * label says, in heading order and then by name, keeping only those whose
 * key or name has query in it. A group left empty isn't shown.
 */
export function groupRepositories(
  repositories: PickerRepository[],
  label: (group: string, sample: PickerRepository) => GroupLabel,
  query = ""
): RepositoryGroup[] {
  const q = query.trim().toLowerCase()
  const groups = new Map<string, RepositoryGroup>()
  for (const r of repositories) {
    if (!matches(r, q)) continue
    let group = groups.get(r.group)
    if (!group) {
      group = { key: r.group, ...label(r.group, r), repositories: [] }
      groups.set(r.group, group)
    }
    group.repositories.push(r)
  }
  return [...groups.values()]
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

/** Servers you run yourself; every other connection form is a cloud. */
export const serverKinds = new Set(["server", "kubernetes", "registry"])

/** What Loomplane runs or provisions on each built-in cloud. */
export const cloudPurposes: Record<string, string> = {
  gcp: "Cloud Run, Cloud SQL, Memorystore and Cloud Storage",
  aws: "ECS on Fargate, RDS, ElastiCache and S3",
  vercel: "Web apps and static sites",
  render: "Web services, workers, Postgres and Key Value",
  digitalocean: "App Platform, Managed PostgreSQL, MySQL and Valkey",
  azure:
    "Container Apps, PostgreSQL and MySQL flexible servers and Managed Redis",
  cloudflare: "DNS zones, domain registration and R2 storage",
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
  repository: PickerRepository
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
 * repositories, grouped, whose keys or names have what's typed in them,
 * each marked added when the form already has its key elsewhere, whatever
 * the case. Once the field holds one of them exactly, all of them show
 * again, so another can take its place.
 */
export function repositoryChoices(
  repositories: PickerRepository[],
  label: (group: string, sample: PickerRepository) => GroupLabel,
  typed: string,
  exclude: string[] = []
): ChoiceGroup[] {
  const t = typed.trim().toLowerCase()
  const exact = repositories.some(
    (r) => r.key.toLowerCase() === t || r.fullName.toLowerCase() === t
  )
  const added = new Set(
    exclude.map((e) => e.trim().toLowerCase()).filter(Boolean)
  )
  return groupRepositories(repositories, label, exact ? "" : t).map((g) => ({
    key: g.key,
    account: g.account,
    avatarUrl: g.avatarUrl,
    items: g.repositories.map((r) => ({
      repository: r,
      added: added.has(r.key.toLowerCase()),
    })),
  }))
}
