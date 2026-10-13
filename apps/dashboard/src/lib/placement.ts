import type { Graph, Manifest, Provider } from "@/client/forge/loomplane/types"

export type PlacementFields = {
  provider?: string
  region?: string
  enabled?: boolean
}
type Entry = PlacementFields & {
  source?: string
  repo?: string
  kind?: string
  environments?: Record<string, PlacementFields>
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
): Required<PlacementFields> {
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
  return { provider, region, enabled: o?.enabled ?? s.enabled ?? true }
}
