import type {
  Build,
  CloudSQLApplicationNetwork,
  Connection,
  Graph,
  Manifest,
  ManifestEndpoint,
  Provider,
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

/** Match the planner's source resolution, including repository qualification. */
export function placementRows(m: Manifest, g: Graph): PlacementRow[] {
  const rows: PlacementRow[] = []
  for (const [name, entry] of Object.entries(m.services ?? {}) as [
    string,
    Entry,
  ][]) {
    const repo =
      entry.repo || (g.sources?.length === 1 ? g.sources[0].alias : "")
    const candidates = g.services.filter((s) => !repo || s.source === repo)
    const local = (s: Graph["services"][number]) => {
      const id = s.id.replace(/^svc:/, "")
      return s.source && id.startsWith(`${s.source}:`)
        ? id.slice(s.source.length + 1)
        : id
    }
    let matches = candidates.filter(
      (s) => local(s) === entry.source || s.entrypoint === entry.source
    )
    if (matches.length === 0)
      matches = candidates.filter((s) => s.path === entry.source)
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
  catalog: Provider[] = []
) {
  const counts = { services: 0, resources: 0, gateways: 0 }
  const clouds = new Set<string>()
  const cloudTargets = new Set<string>()
  for (const section of ["services", "resources", "gateways"] as const) {
    for (const [name, entry] of Object.entries(m[section] ?? {})) {
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
