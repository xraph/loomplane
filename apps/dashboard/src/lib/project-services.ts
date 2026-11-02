import type {
  Binding,
  CloudinfraRoute,
  Environment,
  Excluded,
  Exclusion,
  Graph,
  Service,
} from "@/client/forge/loomplane/types"

/** Where a service stands in one environment. */
export type ServiceState = "live" | "drifted" | "none"

/** ServiceRow is one service the project deploys, as the Services tab shows it. */
export interface ServiceRow {
  service: Service
  /** The binding that runs it, by environment name. */
  deployed: Record<string, Binding | undefined>
  /** Hostnames routed to it, in any environment, then its generated URLs. */
  domains: string[]
  /** The kinds of resource it needs, such as postgres. */
  uses: string[]
}

export type ServiceFilter = "all" | "deployed" | "pending" | "excluded"

/**
 * serviceRows joins the graph's services with what runs in each
 * environment and the domains routed to them. A binding is the manifest's
 * name for a service, which a drafted manifest takes from the graph, so
 * they're matched by name.
 */
export function serviceRows(
  graph: Graph,
  environments: Environment[],
  bindings: Record<string, Binding[] | undefined>,
  routes: CloudinfraRoute[]
): ServiceRow[] {
  const resources = new Map(graph.resources.map((r) => [r.id, r]))
  return graph.services.map((service) => {
    const deployed: Record<string, Binding | undefined> = {}
    for (const e of environments) {
      deployed[e.name] = (bindings[e.name] ?? []).find(
        (b) => b.subject === "service" && b.name === service.name
      )
    }
    const hostnames = routes
      .filter((r) => r.input.service === service.name)
      .map((r) => r.input.hostname)
    const urls = Object.values(deployed).flatMap((b) =>
      b?.url ? [hostOf(b.url)] : []
    )
    const uses = [
      ...new Set(
        graph.edges
          .filter((e) => e.from === service.id && e.kind === "needs")
          .flatMap((e) => {
            const r = resources.get(e.to)
            return r ? [r.kind] : []
          })
      ),
    ]
    return {
      service,
      deployed,
      domains: [...new Set([...hostnames, ...urls])],
      uses,
    }
  })
}

/**
 * locationOf is where a service lives in its repository. A Go service in
 * a module at the root has "." for its path, so its entrypoint, such as
 * cmd/loomctl, says more.
 */
export function locationOf(s: Service): string {
  return s.path && s.path !== "." ? s.path : s.entrypoint || s.path
}

/** stateOf is where the service stands in one environment. */
export function stateOf(binding: Binding | undefined): ServiceState {
  if (!binding) return "none"
  return binding.drift ? "drifted" : "live"
}

/** isDeployed reports whether anything of the service runs anywhere. */
export function isDeployed(row: ServiceRow): boolean {
  return Object.values(row.deployed).some(Boolean)
}

/** filterRows keeps the rows a filter and a search show. */
export function filterRows(
  rows: ServiceRow[],
  filter: Exclude<ServiceFilter, "excluded">,
  search: string
): ServiceRow[] {
  const q = search.trim().toLowerCase()
  return rows.filter((r) => {
    if (filter === "deployed" && !isDeployed(r)) return false
    if (filter === "pending" && isDeployed(r)) return false
    if (!q) return true
    return (
      r.service.name.toLowerCase().includes(q) ||
      locationOf(r.service).toLowerCase().includes(q)
    )
  })
}

/**
 * exclusionOf is the project exclusion that put a service out, matched by
 * the rule the server reports.
 */
export function exclusionOf(
  excluded: Excluded,
  exclusions: Exclusion[]
): Exclusion | undefined {
  return exclusions.find(
    (x) => x.kind === excluded.rule.kind && x.match === excluded.rule.match
  )
}

/**
 * covering is every exclusion that keeps any of services out: its own
 * service exclusion and each path rule over it. Removing only the one the
 * server reports would leave a service still excluded by another.
 */
export function covering(
  services: Service[],
  exclusions: Exclusion[]
): { kind: string; match: string }[] {
  return exclusions
    .filter((x) =>
      services.some((s) =>
        x.kind === "service"
          ? x.match === s.id
          : x.kind === "path" &&
            (matchesPath(x.match, s.path) || matchesPath(x.match, s.entrypoint))
      )
    )
    .map((x) => ({ kind: x.kind, match: x.match }))
}

/**
 * matchesPath mirrors graph.ExclusionRule for path rules, so the dialog can
 * say which other services a rule would take: "a/**" and "a" take a and
 * everything under it, other patterns match one path with * and ?.
 */
export function matchesPath(pattern: string, name: string): boolean {
  const p = trimSlashes(pattern.trim())
  const n = trimSlashes(name)
  if (!p || p === "." || !n || n === ".") return false
  if (p.endsWith("/**")) {
    const dir = p.slice(0, -3)
    return n === dir || n.startsWith(`${dir}/`)
  }
  if (!/[*?[]/.test(p)) return n === p || n.startsWith(`${p}/`)
  const re = new RegExp(
    `^${p
      .split("")
      .map((c) =>
        c === "*"
          ? "[^/]*"
          : c === "?"
            ? "[^/]"
            : c.replace(/[.+^${}()|\\]/g, "\\$&")
      )
      .join("")}$`
  )
  return re.test(n)
}

/**
 * suggestedRule proposes a path rule for the services being excluded: the
 * directory they share, or, for one service, the two directories above
 * it, which is where a group of fixtures or tools usually starts. It
 * proposes nothing at the root or for a top-level service.
 */
export function suggestedRule(services: Service[]): string | undefined {
  if (services.length === 0) return undefined
  const parts = services.map((s) => trimSlashes(locationOf(s)).split("/"))
  let common: string[]
  if (parts.length === 1) {
    common = parts[0].length >= 3 ? parts[0].slice(0, 2) : []
  } else {
    common = []
    for (let i = 0; i < Math.min(...parts.map((p) => p.length)); i++) {
      const seg = parts[0][i]
      if (parts.every((p) => p[i] === seg)) common.push(seg)
      else break
    }
  }
  if (common.length === 0 || (common.length === 1 && common[0] === "cmd"))
    return undefined
  return `${common.join("/")}/**`
}

/** reasonLabel is how the dashboard names an exclusion's reason. */
export function reasonLabel(reason: string): string {
  switch (reason) {
    case "tooling":
      return "Build or CI tooling"
    case "fixture":
      return "Test fixture"
    case "elsewhere":
      return "Deployed elsewhere"
    default:
      return "Something else"
  }
}

function trimSlashes(s: string): string {
  return s.replace(/^\/+|\/+$/g, "")
}

function hostOf(url: string): string {
  try {
    return new URL(url.includes("://") ? url : `https://${url}`).host
  } catch {
    return url
  }
}

/** A link a deployed service's configuration makes to something else. */
export interface ServiceLink {
  key: string
  from: string
  subject: string
}

/** DeployedConfig is what a binding records about how a service runs. */
export interface DeployedConfig {
  env: ServiceLink[]
  port?: number
  cpu?: number
  memoryMB?: number
  healthPath?: string
  image?: string
  framework?: string
  /** For a resource: its kind and version, such as postgres 17. */
  kind?: string
  version?: string
}

/**
 * deployedConfig reads a binding's configuration, which the API sends as
 * an object or a JSON string with snake_case keys. Anything unreadable
 * reads as empty.
 */
export function deployedConfig(binding: Binding | undefined): DeployedConfig {
  const raw = configObject(binding)
  const str = (v: unknown) => (typeof v === "string" && v ? v : undefined)
  const num = (v: unknown) => (typeof v === "number" ? v : undefined)
  const size = isRecord(raw.size) ? raw.size : {}
  const env = Array.isArray(raw.env)
    ? raw.env.flatMap((l: unknown) =>
        isRecord(l) && typeof l.key === "string" && typeof l.from === "string"
          ? [
              {
                key: l.key,
                from: l.from,
                subject: typeof l.subject === "string" ? l.subject : "",
              },
            ]
          : []
      )
    : []
  return {
    env,
    port: num(raw.port),
    cpu: num(size.cpu),
    memoryMB: num(size.memory),
    healthPath: str(raw.health_path ?? raw.healthPath),
    image: str(raw.image),
    framework: str(raw.framework),
    kind: str(raw.kind),
    version: str(raw.version),
  }
}

/**
 * callersOf lists the deployed services whose configuration links to
 * service, each with the variable it's linked through.
 */
export function callersOf(
  service: string,
  bindings: Binding[]
): { binding: Binding; key: string }[] {
  return bindings.flatMap((b) => {
    if (b.subject !== "service" || b.name === service) return []
    const link = deployedConfig(b).env.find(
      (l) => l.subject === "service" && l.from === service
    )
    return link ? [{ binding: b, key: link.key }] : []
  })
}

/** sizeLabel names a deployed size, such as "1 vCPU, 1 GB". */
export function sizeLabel(c: DeployedConfig): string {
  const parts: string[] = []
  if (c.cpu) parts.push(`${c.cpu} vCPU`)
  if (c.memoryMB)
    parts.push(
      c.memoryMB >= 1024 && c.memoryMB % 1024 === 0
        ? `${c.memoryMB / 1024} GB`
        : `${c.memoryMB} MB`
    )
  return parts.join(", ")
}

function configObject(binding: Binding | undefined): Record<string, unknown> {
  if (!binding) return {}
  const raw: unknown = binding.config
  try {
    const parsed: unknown = typeof raw === "string" ? JSON.parse(raw) : raw
    return isRecord(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v)
}

/**
 * enabledIn reads whether a manifest service is on in an environment: on
 * unless that environment's override turns it off.
 */
export function enabledIn(service: unknown, environment: string): boolean {
  if (typeof service !== "object" || service === null) return true
  const envs = (service as { environments?: Record<string, unknown> })
    .environments
  const override = envs?.[environment]
  if (typeof override !== "object" || override === null) return true
  return (override as { enabled?: unknown }).enabled !== false
}
