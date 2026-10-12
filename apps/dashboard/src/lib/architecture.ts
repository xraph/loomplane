// What a repository analysis found, made readable: labels for the API's
// kinds, links from each piece of evidence to the line on GitHub, and the
// packages and resources each service connects to.

import type {
  Analysis,
  Evidence,
  Graph,
  Package,
  Resource,
  Service,
} from "@/client/forge/loomplane/types"

/** The statuses of an analysis that hasn't finished, in order. */
export const steps = ["queued", "fetching", "analyzing"] as const

/** isActive reports whether an analysis is still running. */
export function isActive(status: string): boolean {
  return (steps as readonly string[]).includes(status)
}

/** stepIndex is how far along a running analysis is, or -1. */
export function stepIndex(status: string): number {
  return (steps as readonly string[]).indexOf(status)
}

const stepLabels: Record<string, string> = {
  queued: "Waiting for a worker",
  fetching: "Downloading the commit",
  analyzing: "Reading manifests and code",
}

/** stepLabel says what a running analysis is doing. */
export function stepLabel(status: string): string {
  return stepLabels[status] ?? status
}

const kindLabels: Record<string, string> = {
  "http-service": "HTTP service",
  "web-app": "Web app",
  "static-site": "Static site",
  worker: "Worker",
  tool: "Command-line tool",
  "native-app": "Native app",
}

/** kindLabel names a service kind for people. */
export function kindLabel(kind: string): string {
  return kindLabels[kind] ?? kind
}

const ecosystemLabels: Record<string, string> = {
  go: "Go",
  rust: "Rust",
  node: "Node",
  container: "Container",
}

/** ecosystemLabel names an ecosystem for people. */
export function ecosystemLabel(ecosystem: string): string {
  return ecosystemLabels[ecosystem] ?? ecosystem
}

const resourceLabels: Record<string, string> = {
  postgres: "Postgres",
  mysql: "MySQL",
  redis: "Redis",
  mongodb: "MongoDB",
  rabbitmq: "RabbitMQ",
  kafka: "Kafka",
  nats: "NATS",
  "object-storage": "Object storage",
  search: "Search",
  memcached: "Memcached",
  clickhouse: "ClickHouse",
  sqlserver: "SQL Server",
}

/** resourceLabel names a backing service for people. */
export function resourceLabel(kind: string): string {
  return resourceLabels[kind] ?? kind
}

/** shortSHA is the seven characters people recognize a commit by. */
export function shortSHA(sha: string): string {
  return sha.slice(0, 7)
}

/**
 * evidenceURL links a finding to the line it was read from, at the exact
 * commit analyzed, so the link still shows the same code later.
 */
export function evidenceURL(
  repository: string,
  sha: string,
  ev: Evidence
): string {
  const path = ev.path.split("/").map(encodeURIComponent).join("/")
  const line = ev.line ? `#L${ev.line}` : ""
  return `https://github.com/${repository}/blob/${sha}/${path}${line}`
}

/** SourceLink is a source as a graph or a plan names it: enough to link into it. */
export interface SourceLink {
  alias: string
  repository: string
  commitSha: string
}

/**
 * sourceOf finds the source a finding was read from. A graph of one
 * source may not name it, so the only one is the answer then.
 */
export function sourceOf(
  sources: SourceLink[],
  alias?: string
): SourceLink | undefined {
  if (alias) return sources.find((s) => s.alias === alias)
  return sources.length === 1 ? sources[0] : undefined
}

/** linkTo links a finding to its line in whichever source it came from, or "" when that isn't known. */
export function linkTo(sources: SourceLink[], ev: Evidence): string {
  const s = sourceOf(sources, ev.source)
  return s?.commitSha ? evidenceURL(s.repository, s.commitSha, ev) : ""
}

/** lockLabel names the commits something was read from: "web@3f2c1a9 · api@9a1b02e". */
export function lockLabel(
  sources: { alias: string; commitSha: string }[]
): string {
  return sources.map((s) => `${s.alias}@${shortSHA(s.commitSha)}`).join(" · ")
}

/** clusters groups a graph's services by the source they came from, in alias order. */
export function clusters(
  graph: Graph
): { alias: string; services: Service[] }[] {
  const by = new Map<string, Service[]>()
  for (const s of graph.services) {
    const alias = s.source ?? ""
    by.set(alias, [...(by.get(alias) ?? []), s])
  }
  return [...by.keys()]
    .sort()
    .map((alias) => ({ alias, services: by.get(alias)! }))
}

/** commitURL links to the commit an analysis read. */
export function commitURL(repository: string, sha: string): string {
  return `https://github.com/${repository}/commit/${sha}`
}

/** evidenceLabel is "path:line", or the path alone. */
export function evidenceLabel(ev: Evidence): string {
  return ev.line ? `${ev.path}:${ev.line}` : ev.path
}

export interface Need {
  resource: Resource
  confidence: string
  /** via names the package the need comes through, when it isn't direct. */
  via?: string
}

export interface Connections {
  uses: Package[]
  needs: Need[]
}

/**
 * connections lists the packages a service builds with directly, and every
 * resource it needs: its own, then those of the packages it uses, however
 * deep, each marked with the package it comes through.
 */
export function connections(graph: Graph, serviceID: string): Connections {
  const packages = new Map(graph.packages.map((p) => [p.id, p]))
  const resources = new Map(graph.resources.map((r) => [r.id, r]))
  const out: Connections = { uses: [], needs: [] }
  const needed = new Set<string>()

  const visit = (from: string, via?: string) => {
    for (const e of graph.edges) {
      if (e.from !== from || e.kind !== "needs") continue
      const res = resources.get(e.to)
      if (res && !needed.has(res.id)) {
        needed.add(res.id)
        out.needs.push(
          via
            ? { resource: res, confidence: e.confidence, via }
            : { resource: res, confidence: e.confidence }
        )
      }
    }
  }

  visit(serviceID)
  const seen = new Set<string>()
  const queue: string[] = []
  for (const e of graph.edges) {
    const pkg = packages.get(e.to)
    if (e.from === serviceID && e.kind === "uses" && pkg) {
      out.uses.push(pkg)
      queue.push(pkg.id)
    }
  }
  while (queue.length > 0) {
    const id = queue.shift() as string
    if (seen.has(id)) continue
    seen.add(id)
    visit(id, packages.get(id)?.name)
    for (const e of graph.edges) {
      if (e.from === id && e.kind === "uses" && packages.has(e.to)) {
        queue.push(e.to)
      }
    }
  }
  return out
}

/** latestBySource is each source's newest analysis, from a list newest first. */
export function latestBySource(analyses: Analysis[]): Map<string, Analysis> {
  const out = new Map<string, Analysis>()
  for (const a of analyses) if (!out.has(a.source)) out.set(a.source, a)
  return out
}

/**
 * lastRead says what a source's newest analysis did: still running, failed,
 * or which commit it read. It takes the project graph's state for the
 * source, which knows its newest analysis however long ago that was.
 */
export function lastRead(state?: { latest?: Analysis }): string {
  const a = state?.latest
  if (!a) return "Not analyzed"
  if (isActive(a.status)) return stepLabel(a.status)
  if (a.status === "failed") return "Failed"
  return `@${shortSHA(a.commitSha)}`
}

/**
 * spansSources says whether a list names more than one source, so a table
 * only spends a column on the repository when it tells rows apart.
 */
export function spansSources(items: { source?: string }[]): boolean {
  return new Set(items.map((i) => i.source ?? "")).size > 1
}

/** latestSucceeded is the newest analysis that produced a graph. */
export function latestSucceeded(analyses: Analysis[]): Analysis | undefined {
  return analyses.find((a) => a.status === "succeeded")
}
