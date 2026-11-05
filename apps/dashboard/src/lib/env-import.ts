// What an import of variables starts from: the ones an analysis found a
// service reading, or the lines of a pasted .env. Each becomes a row
// someone checks, fills in and sets.

import type {
  EnvUse,
  DiscoveryEvidence as Evidence,
  Graph,
} from "@/client/forge/loomplane/types"
import { parseDotenv } from "@/lib/dotenv"
import { looksSecret } from "@/lib/variables"

/** Keys Loomplane sets on every deployment, so no one needs to. */
export const platformKeys = new Set(["PORT"])

/**
 * The address variable Loomplane gives a service for each resource kind it
 * needs. Mirrors envKeys in internal/planning/planner.go.
 */
export const resourceKeys: Record<string, string> = {
  postgres: "DATABASE_URL",
  mysql: "DATABASE_URL",
  redis: "REDIS_URL",
  memcached: "MEMCACHED_SERVERS",
  mongodb: "MONGODB_URI",
  rabbitmq: "AMQP_URL",
  kafka: "KAFKA_BROKERS",
  nats: "NATS_URL",
  "object-storage": "STORAGE_URL",
  search: "SEARCH_URL",
  clickhouse: "CLICKHOUSE_URL",
}

/** Detected is a variable the analysis found, and what may set it already. */
export interface Detected extends EnvUse {
  /**
   * The resource whose address Loomplane gives the service under this key,
   * when the analysis thinks it needs one, such as "postgres".
   */
  providedBy?: string
}

export interface ImportRow {
  key: string
  value: string
  secret: boolean
  include: boolean
  /** Where an analysis found it, for a detected row. */
  evidence?: Evidence[]
  /** The value an example file or the code gives it, for a detected row. */
  example?: string
  /** The resource Loomplane sets it for, for a detected row. */
  providedBy?: string
}

/**
 * detected lists the variables the analysis found the target's services
 * reading: one service's, or every deployable service's for the whole
 * project, a key read by several listed once. A key Loomplane sets for a
 * resource the service needs says which.
 */
export function detected(
  graph: Graph | undefined,
  service: string
): Detected[] {
  const services = graph?.services ?? []
  const from = service
    ? services.filter((s) => s.id === service)
    : services.filter((s) => s.deployable)
  const byName = new Map<string, Detected>()
  for (const s of from) {
    const provided = new Map<string, string>()
    for (const e of graph?.edges ?? []) {
      const kind = e.to.replace(/^res:/, "")
      if (e.from === s.id && e.kind === "needs" && resourceKeys[kind]) {
        provided.set(resourceKeys[kind], kind)
      }
    }
    for (const v of s.env ?? []) {
      if (platformKeys.has(v.name)) continue
      const seen = byName.get(v.name)
      if (!seen) {
        byName.set(v.name, {
          ...v,
          evidence: [...v.evidence],
          providedBy: provided.get(v.name),
        })
        continue
      }
      seen.providedBy ||= provided.get(v.name)
      seen.evidence.push(...v.evidence)
      seen.example ||= v.example
    }
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * rowsFromDetected makes a row for each detected variable not set yet. A
 * secret starts empty even when an example gives a value: an example's
 * secret is a placeholder, such as "changeme", never the one to deploy.
 */
export function rowsFromDetected(
  vars: Detected[],
  set: Set<string>
): ImportRow[] {
  return vars
    .filter((v) => !set.has(v.name))
    .map((v) => {
      const secret = looksSecret(v.name, v.example)
      return {
        key: v.name,
        value: secret ? "" : (v.example ?? ""),
        secret,
        // Unticked when Loomplane will set it: setting it would win over
        // the address Loomplane gives.
        include: !v.providedBy,
        evidence: v.evidence,
        example: v.example,
        providedBy: v.providedBy,
      }
    })
}

/** rowsFromPaste makes a row for each line of pasted .env content. */
export function rowsFromPaste(text: string) {
  const { entries, problems } = parseDotenv(text)
  const rows: ImportRow[] = entries.map((e) => ({
    key: e.key,
    value: e.value,
    secret: looksSecret(e.key, e.value),
    include: true,
  }))
  return { rows, problems }
}

/** where says in a few words where a detected variable is read. */
export function whereRead(evidence: Evidence[] = []): string {
  if (evidence.length === 0) return ""
  const first = evidence[0]
  const at = first.line ? `${first.path}:${first.line}` : first.path
  return evidence.length > 1 ? `${at} and ${evidence.length - 1} more` : at
}
