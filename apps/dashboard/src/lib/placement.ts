import type {
  Binding,
  Build,
  CloudSQLApplicationNetwork,
  Connection,
  Excluded,
  Graph,
  Manifest,
  ManifestEndpoint,
  Provider,
  Target,
} from "@/client/forge/loomplane/types"

export type PlacementFields = {
  cloudSqlNetwork?: CloudSQLApplicationNetwork
  cloud_sql_network?: CloudSQLApplicationNetwork & { consumer_project?: string }
  cloudTarget?: string
  cloud_target?: string
  cloudTags?: string[]
  cloud_tags?: string[]
  provider?: string
  region?: string
  enabled?: boolean
  target?: string
  build?: Build
  endpoint?: ManifestEndpoint
  healthPath?: string
}
type Entry = PlacementFields & {
  source?: string
  repo?: string
  kind?: string
  health_path?: string
  environments?: Record<string, PlacementFields & { health_path?: string }>
}
export type PlacementRow = {
  section: "services" | "resources" | "gateways"
  name: string
  kind: string
  entry: Entry
}

const localID = (s: Graph["services"][number]) => {
  const id = s.id.replace(/^svc:/, "")
  return s.source && id.startsWith(`${s.source}:`)
    ? id.slice(s.source.length + 1)
    : id
}

// The graph services a manifest entry names, by its source within its repo.
function matching(services: Graph["services"], entry: Entry, repo: string) {
  const candidates = services.filter((s) => !repo || s.source === repo)
  const named = candidates.filter(
    (s) => localID(s) === entry.source || s.entrypoint === entry.source
  )
  return named.length
    ? named
    : candidates.filter((s) => s.path === entry.source)
}

const repoOf = (entry: Entry, g: Graph) =>
  entry.repo || (g.sources?.length === 1 ? g.sources[0].alias : "")

/**
 * excludedNames are the manifest's services the project excludes. The
 * project graph leaves them out, so one is an entry that matches nothing
 * kept but does match an excluded service. The planner skips them, so a
 * deploy never offers or counts them.
 */
export function excludedNames(
  m: Manifest,
  g: Graph,
  excluded: Excluded[]
): Set<string> {
  const gone = excluded.map((x) => x.service)
  const out = new Set<string>()
  if (!gone.length) return out
  for (const [name, entry] of Object.entries(m.services ?? {}) as [
    string,
    Entry,
  ][]) {
    const repo = repoOf(entry, g)
    if (
      !matching(g.services, entry, repo).length &&
      matching(gone, entry, repo).length
    )
      out.add(name)
  }
  return out
}

/** Match the planner's source resolution, including repository qualification. */
export function placementRows(
  m: Manifest,
  g: Graph,
  /** Services the project excludes; see excludedNames. */
  excluded: Excluded[] = []
): PlacementRow[] {
  const rows: PlacementRow[] = []
  const skip = excludedNames(m, g, excluded)
  for (const [name, entry] of Object.entries(m.services ?? {}) as [
    string,
    Entry,
  ][]) {
    if (skip.has(name)) continue
    const repo = repoOf(entry, g)
    const matches = matching(g.services, entry, repo)
    const found =
      matches.length === 1 && (!g.sources || g.sources.length < 2 || repo)
        ? matches[0]
        : undefined
    rows.push({
      section: "services",
      name,
      entry,
      kind:
        found && (entry.kind || found.deployable)
          ? entry.kind || found.kind
          : "",
    })
  }
  for (const [name, entry] of Object.entries(m.resources ?? {}) as [
    string,
    Entry,
  ][]) {
    rows.push({ section: "resources", name, entry, kind: entry.kind ?? "" })
  }
  for (const [name, entry] of Object.entries(m.gateways ?? {}) as [
    string,
    Entry,
  ][]) {
    rows.push({ section: "gateways", name, entry, kind: "http-service" })
  }
  return rows
}

/** Only offer targets that are ready and support this workload or resource. */
export function compatibleTargets(
  row: PlacementRow,
  catalog: Provider[]
): Provider[] {
  return catalog.filter(
    (p) =>
      p.available &&
      (row.section !== "gateways" ||
        p.name === "cloud-run" ||
        p.name === "ecs-fargate") &&
      (row.section === "resources"
        ? p.provisions?.some((r) => r.kind === row.kind)
        : p.runs?.some((r) => r.workload === row.kind))
  )
}

/** Keep region inheritance identical to manifest.Service.For on the server. */
export function placementSettings(
  m: Manifest,
  row: PlacementRow,
  environment: string
): PlacementFields & { provider: string; region: string; enabled: boolean } {
  const d = m.defaults
  const s = row.entry
  let provider = s.provider || d?.provider || ""
  let region = s.region || (provider === d?.provider ? d?.region || "" : "")
  const o = s.environments?.[environment]
  if (o?.provider && o.provider !== provider) {
    provider = o.provider
    region = provider === d?.provider ? d?.region || "" : ""
  }
  if (o?.region) region = o.region
  const changedProvider =
    !!o?.provider && o.provider !== (s.provider || d?.provider)
  const rawRoute =
    o?.cloudSqlNetwork ??
    o?.cloud_sql_network ??
    (changedProvider &&
    (s.provider || d?.provider || o?.provider !== "cloud-run")
      ? undefined
      : (s.cloudSqlNetwork ?? s.cloud_sql_network))
  const cloudSqlNetwork = rawRoute
    ? {
        consumerProject:
          rawRoute.consumerProject ??
          (rawRoute as { consumer_project?: string }).consumer_project ??
          "",
        network: rawRoute.network,
        subnetwork: rawRoute.subnetwork,
      }
    : undefined
  const target = o?.target || (changedProvider ? undefined : s.target)
  const changedTarget = !!o?.target && o.target !== s.target
  const rawEndpoint =
    o?.endpoint ?? (changedProvider || changedTarget ? undefined : s.endpoint)
  // Manifest maps are opaque JSON in the generated contract. Normalize their
  // snake_case members at this boundary; placement writes use generated codecs.
  const raw = rawEndpoint as
    | (ManifestEndpoint & {
        host_port?: number
        ingress_class?: string
        tls_secret?: string
      })
    | undefined
  const endpoint = raw
    ? {
        hostPort: raw.hostPort ?? raw.host_port,
        hostname: raw.hostname,
        ingressClass: raw.ingressClass ?? raw.ingress_class,
        tlsSecret: raw.tlsSecret ?? raw.tls_secret,
      }
    : undefined
  const build = o?.build ?? s.build
  const healthPath =
    o?.healthPath ?? o?.health_path ?? s.healthPath ?? s.health_path
  // An environment names its target one way, replacing the service's choice.
  // Default tags reach only a service with no placement of its own.
  let cloudTarget = s.cloudTarget || s.cloud_target || ""
  let cloudTags = tagsOf(s)
  const overrideTarget = o?.cloudTarget || o?.cloud_target || ""
  const overrideTags = o ? tagsOf(o) : []
  if (overrideTarget) [cloudTarget, cloudTags] = [overrideTarget, []]
  if (overrideTags.length) [cloudTarget, cloudTags] = ["", overrideTags]
  const defaultTags =
    d?.cloudTags ?? (d as { cloud_tags?: string[] })?.cloud_tags
  if (!cloudTarget && !cloudTags.length && !provider && !target)
    cloudTags = [...(defaultTags ?? [])]
  return {
    ...(cloudTarget ? { cloudTarget } : {}),
    ...(cloudTags.length ? { cloudTags } : {}),
    provider,
    region,
    enabled: o?.enabled ?? s.enabled ?? true,
    ...(cloudSqlNetwork ? { cloudSqlNetwork } : {}),
    ...(target ? { target } : {}),
    ...(endpoint ? { endpoint } : {}),
    ...(build ? { build } : {}),
    ...(healthPath ? { healthPath } : {}),
  }
}

function tagsOf(fields: PlacementFields): string[] {
  return [...(fields.cloudTags ?? fields.cloud_tags ?? [])]
}

export function targetConnections(
  connections: Connection[],
  cloud: string
): Connection[] {
  return connections.filter((c) => c.cloud === cloud && c.status === "ready")
}

/** The effective placement in one environment, shared by summaries and account selection. */
export function placementSummary(
  m: Manifest,
  environment: string,
  catalog: Provider[] = [],
  /** Services to leave out of the counts, such as excludedNames. */
  skip: Set<string> = new Set()
) {
  const counts = { services: 0, resources: 0, gateways: 0 }
  const clouds = new Set<string>()
  const cloudTargets = new Set<string>()
  for (const section of ["services", "resources", "gateways"] as const) {
    for (const [name, entry] of Object.entries(m[section] ?? {})) {
      if (section === "services" && skip.has(name)) continue
      const settings = placementSettings(
        m,
        { section, name, kind: "", entry },
        environment
      )
      if (!settings.enabled) continue
      counts[section]++
      if (settings.cloudTarget) cloudTargets.add(settings.cloudTarget)
      const cloud = catalog.find((p) => p.name === settings.provider)?.cloud
      if (cloud) clouds.add(cloud)
    }
  }
  for (const link of Object.values(m.links ?? {})) {
    if (link.environments?.[environment]?.enabled !== false)
      for (const cloud of link.between ?? []) clouds.add(String(cloud))
  }
  return {
    ...counts,
    cloudTargets: [...cloudTargets].sort(),
    clouds: [...clouds].sort(),
  }
}

/** rowKey names one placement row, unique across a manifest's sections. */
export const rowKey = (row: Pick<PlacementRow, "section" | "name">) =>
  `${row.section}:${row.name}`

/**
 * cloudChoice is the Cloud target a placement edit picks, by name or by
 * tags, if it picks one. Adopting one rewrites the whole manifest.
 */
export function cloudChoice(
  changes: PlacementFields
): string | { tags: string[] } | undefined {
  if (changes.cloudTarget) return changes.cloudTarget
  if (changes.cloudTags?.length) return { tags: changes.cloudTags }
  return undefined
}

// Fields a Cloud target choice replaces: adoption rewrites them itself.
export const cloudOwnedFields = [
  "cloudTarget",
  "cloudTags",
  "provider",
  "region",
  "target",
  "endpoint",
  "cloudSqlNetwork",
] as const

/**
 * applyPlacement makes the same change the API's placement edit makes, to a
 * manifest in memory: each field set replaces the environment's override, an
 * empty value restores inheritance, and nothing else moves. It lets several
 * edits land as one revision.
 */
export function applyPlacement(
  m: Manifest,
  row: Pick<PlacementRow, "section" | "name">,
  environment: string,
  fields: PlacementFields
): Manifest {
  const next = structuredClone(m)
  const entry = (next[row.section] as Record<string, Entry> | undefined)?.[
    row.name
  ]
  if (!entry)
    throw new Error(`${row.name} isn't in loomplane.yaml anymore. Reload it.`)
  const o: Record<string, unknown> = {
    ...entry.environments?.[environment],
  }
  const put = (key: string, value: unknown, empty: boolean) => {
    if (empty) delete o[key]
    else o[key] = value
  }
  for (const key of ["provider", "region", "target"] as const) {
    const v = fields[key]
    if (v !== undefined) put(key, v, !v)
  }
  if (fields.cloudTarget !== undefined) {
    delete o.cloudTarget
    put("cloud_target", fields.cloudTarget, !fields.cloudTarget)
  }
  if (fields.cloudTags !== undefined) {
    delete o.cloudTags
    put("cloud_tags", [...fields.cloudTags], !fields.cloudTags.length)
  }
  if (fields.healthPath !== undefined) {
    delete o.healthPath
    put("health_path", fields.healthPath, !fields.healthPath)
  }
  if (fields.endpoint !== undefined) {
    const e = compact({
      host_port: fields.endpoint.hostPort,
      hostname: fields.endpoint.hostname,
      ingress_class: fields.endpoint.ingressClass,
      tls_secret: fields.endpoint.tlsSecret,
    })
    put("endpoint", e, !Object.keys(e).length)
  }
  if (fields.cloudSqlNetwork !== undefined) {
    delete o.cloudSqlNetwork
    const r = compact({
      consumer_project: fields.cloudSqlNetwork.consumerProject,
      network: fields.cloudSqlNetwork.network,
      subnetwork: fields.cloudSqlNetwork.subnetwork,
    })
    put("cloud_sql_network", r, !Object.keys(r).length)
  }
  if (fields.build !== undefined) {
    const b = compact({ ...fields.build })
    put("build", b, !Object.keys(b).length)
  }
  if (fields.enabled !== undefined) o.enabled = fields.enabled
  entry.environments = { ...entry.environments, [environment]: o }
  return next
}

// compact drops unset and empty members, as the API's omitempty does.
function compact<T extends Record<string, unknown>>(v: T): Partial<T> {
  return Object.fromEntries(
    Object.entries(v).filter(([, x]) => x !== undefined && x !== "" && x !== 0)
  ) as Partial<T>
}

/** RunsOn says where a row runs in one environment, in two short lines. */
export type RunsOn = {
  /** The provider's cloud, for its glyph. */
  cloud?: string
  name: string
  where: string
}

/**
 * runsOn names where a placement puts a service: its Cloud target, the
 * account or provider it runs on directly, or that it's off.
 */
export function runsOn(
  value: ReturnType<typeof placementSettings>,
  environment: string,
  catalog: Provider[],
  cloudTargets: Target[],
  connections: Connection[]
): RunsOn {
  const provider = (name: string) => catalog.find((p) => p.name === name)
  if (!value.enabled) return { name: `Off in ${environment}`, where: "" }
  if (value.cloudTarget) {
    const t = cloudTargets.find((x) => x.name === value.cloudTarget)
    const p = t ? provider(t.provider) : undefined
    return {
      cloud: p?.cloud,
      name: t?.displayName || value.cloudTarget,
      where: t
        ? [p?.title ?? t.provider, t.region].filter(Boolean).join(" · ")
        : "Cloud target",
    }
  }
  if (value.cloudTags?.length)
    return {
      name: `Target tagged ${value.cloudTags.join(", ")}`,
      where: "Cloud target, by tag",
    }
  if (value.provider) {
    const p = provider(value.provider)
    const title = p?.title ?? value.provider
    const account = value.target
      ? connections.find((c) => c.id === value.target)?.name
      : undefined
    return {
      cloud: p?.cloud,
      name: account ?? title,
      where: [account ? title : "", value.region || "automatic region"]
        .filter(Boolean)
        .join(" · "),
    }
  }
  return { name: "Not placed yet", where: "Choose where it runs" }
}

/** SinceLastDeploy is how a service differs from what an environment runs. */
export type SinceLastDeploy = {
  label: string
  tone: "flight" | "waiting" | "quiet"
  /** changed services are picked by default. */
  changed: boolean
}

/**
 * sinceLastDeploy compares a service with what runs: one that doesn't run
 * yet is new, one built from an older commit of its repository changed,
 * and one that drifted from its deploy is worth deploying again.
 */
export function sinceLastDeploy(
  row: PlacementRow,
  g: Graph | undefined,
  binding: Binding | undefined,
  enabled: boolean
): SinceLastDeploy {
  if (!enabled) return { label: "Turned off", tone: "quiet", changed: false }
  if (!binding) return { label: "New here", tone: "flight", changed: true }
  if (binding.drift) return { label: "Drifted", tone: "waiting", changed: true }
  const repo = g ? repoOf(row.entry, g) : ""
  const commit = g?.sources?.find((s) => s.alias === repo)?.commitSha
  if (commit && binding.commitSha && commit !== binding.commitSha)
    return { label: "New commits", tone: "flight", changed: true }
  return { label: "No changes", tone: "quiet", changed: false }
}
