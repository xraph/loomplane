// What the Add Fabriq dialog decides: how apps use Fabriq, where each
// engine comes from, and the words that describe it.
import { agoDay } from "@/lib/relative-time"
export { ago, agoDay, createdLabel } from "@/lib/relative-time"
import { statusOf } from "@/client/errors"
import type { CheckResult } from "@/client/forge/loomplane/types"
import type { AddCatalogServiceInput } from "@/lib/catalog-add"

/** How apps use Fabriq: a hosted server, or a library their Go app embeds. */
export type FabriqMode = "server" | "library"

/** Where an engine comes from. Postgres and Redis can't be left off. */
export type FabriqSource = "new" | "existing"
export type FabriqProjection = "off" | "new"

export type FabriqChoice = {
  mode: FabriqMode
  pg: FabriqSource
  redis: FabriqSource
  falkor: FabriqProjection
  es: FabriqProjection
}

/** fabriqKindFor is the catalog kind that runs Fabriq in a mode. */
export function fabriqKindFor(mode: FabriqMode): string {
  return mode === "server" ? "fabriq" : "fabriq-library"
}

/** fabriqSummary lists what the plan will create, as the dialog words it. */
export function fabriqSummary(choice: FabriqChoice): string {
  const parts = [
    choice.pg === "new"
      ? "Postgres 16 with TimescaleDB, pgvector and PostGIS"
      : "a link to your Postgres",
    choice.redis === "new" ? "Redis" : "a link to your Redis",
  ]
  if (choice.falkor === "new") parts.push("FalkorDB")
  if (choice.es === "new") parts.push("Elasticsearch")
  parts.push(
    "a migration job",
    choice.mode === "server"
      ? "the Fabriq server and the admin console"
      : "the Fabriq worker"
  )
  const text = `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}.`
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** Where Postgres or Redis is read from when it isn't provisioned new. */
export type FabriqLink = { resource?: string; external?: string }

/**
 * fabriqUses is the manifest's uses for the choices. Each projection is
 * named either way, so the manifest says plainly whether it is on.
 */
export function fabriqUses(
  choice: FabriqChoice,
  links: { postgres?: FabriqLink; redis?: FabriqLink }
): NonNullable<AddCatalogServiceInput["uses"]> {
  const uses: NonNullable<AddCatalogServiceInput["uses"]> = {}
  if (choice.pg === "existing" && links.postgres)
    uses.postgres = { ...links.postgres }
  if (choice.redis === "existing" && links.redis)
    uses.redis = { ...links.redis }
  uses.falkordb = { enabled: choice.falkor === "new" }
  uses.elasticsearch = { enabled: choice.es === "new" }
  return uses
}

export type CheckEngine = "postgres" | "redis"

const engineTitles: Record<CheckEngine, string> = {
  postgres: "PostgreSQL",
  redis: "Redis",
}

/**
 * checkMessage words what a connection check found. Everything but a clean
 * pass is a warning; an endpoint that wasn't reached shows its first
 * problem alone.
 */
export function checkMessage(
  engine: CheckEngine,
  result: CheckResult
): { tone: "ok" | "warn"; text: string } {
  const problems = result.problems ?? []
  const missing = result.missing ?? []
  if (!result.version)
    return { tone: "warn", text: problems[0] ?? "The check didn't finish." }
  const connected = `Connected to ${engineTitles[engine]} ${result.version}.`
  if (result.ok) return { tone: "ok", text: `${connected} Ready to use.` }
  const said: string[] = []
  if (missing.length > 0)
    said.push(
      `Missing extension: ${missing.join(", ")}. Ask your database admin to install it, or provision a new one.`
    )
  said.push(...problems)
  return { tone: "warn", text: [connected, ...said].join(" ") }
}

const tenantPattern = /^[a-z0-9][a-z0-9-]{0,62}$/

/**
 * validTenantId says whether Loomplane accepts a tenant ID: lowercase
 * letters, digits and dashes, starting with a letter or digit, at most 63
 * characters. It is the rule the API applies, which is stricter than
 * Fabriq's own.
 */
export function validTenantId(id: string): boolean {
  return tenantPattern.test(id)
}

/**
 * unreachableReason is why Fabriq can't be reached, when a failed call says
 * so: the API answers 503 with "Fabriq isn't reachable right now: <reason>"
 * when the server or its console is down. It is undefined for any other
 * failure, which is not about Fabriq being down.
 */
export function unreachableReason(error: unknown): string | undefined {
  if (statusOf(error) !== 503) return undefined
  const message = error instanceof Error ? error.message : ""
  const said = message.replace(/^Fabriq isn't reachable right now:?\s*/i, "")
  return sentence(said)
}

/** unreachableText is how the panel words Fabriq being down. */
export function unreachableText(reason?: string): string {
  const said = sentence(reason ?? "")
  return said
    ? `Fabriq isn't reachable right now. ${said}`
    : "Fabriq isn't reachable right now."
}

function sentence(text: string): string {
  const t = text.trim()
  if (!t) return ""
  const s = t.charAt(0).toUpperCase() + t.slice(1)
  return /[.!?]$/.test(s) ? s : `${s}.`
}

export type StackState =
  "Running" | "Completed" | "Off" | "Not running" | "External" | "Linked"

export type StackPart = {
  id:
    | "postgres"
    | "redis"
    | "migrations"
    | "worker"
    | "server"
    | "admin"
    | "falkordb"
    | "elasticsearch"
  name: string
  state: StackState
  detail: string
}

export type EngineSource = "managed" | "resource" | "external"

export type StackInput = {
  library?: boolean
  /** The resource's provider: "server" is Docker, "kubernetes" a cluster. */
  provider: string
  /**
   * Where Postgres and Redis come from: provisioned with Fabriq ("managed"),
   * another resource in this project ("resource", which Loomplane runs), or
   * an endpoint Loomplane doesn't run ("external").
   */
  postgres: EngineSource
  redis: EngineSource
  /** The resource's name, for an engine that is a project resource. */
  postgresResource?: string
  redisResource?: string
  /** The saved policy, including operator overrides. */
  backupPolicy?: { enabled: boolean; everyHours: number; lastError?: string }
  backupPolicyStatus?: "loading" | "unavailable" | "denied"
  /** When the newest test restore of Postgres was verified, if one has been. */
  restoreVerifiedAt?: string
  /** Set while the backups and restores are still loading, or couldn't be read. */
  restoreStatus?: "loading" | "unavailable"
  now: number
  /** Which parts are deployed and in step. A part left out counts as deployed. */
  down?: StackPart["id"][]
  linked?: Partial<Record<StackPart["id"], "External" | "Linked">>
  falkordb: boolean
  elasticsearch: boolean
  /** How many migrations Fabriq reports applied, when it reports them. */
  migrations?: number
  /** How many dynamic entity types its server has loaded, when it reports them. */
  entityTypes?: number
}

const postgresExtensions = "TimescaleDB, pgvector, PostGIS."

/** What an external engine says on the Stack and Engines cards. */
const externalSentence = "Not managed by Loomplane: no backups or restores."

/** What the Postgres card and the Add Fabriq dialog say about backups on a cluster. */
export const kubernetesBackupsSentence =
  "Backups on Kubernetes aren’t available yet."

function postgresDetail(input: StackInput): string {
  if (input.postgres === "external")
    return `${postgresExtensions} ${externalSentence}`
  if (input.postgres === "resource")
    return `${postgresExtensions} Backed up with ${input.postgresResource ?? "its resource"}.`
  if (input.provider === "kubernetes")
    return `${postgresExtensions} ${kubernetesBackupsSentence}`
  const schedule = backupSchedule(input)
  if (input.restoreStatus === "loading")
    return `${postgresExtensions} ${schedule}.`
  if (input.restoreStatus === "unavailable")
    return `${postgresExtensions} ${schedule}, restore history unavailable.`
  return input.restoreVerifiedAt
    ? `${postgresExtensions} ${schedule}, last restore verified ${agoDay(input.restoreVerifiedAt, input.now)}.`
    : `${postgresExtensions} ${schedule}, no restore verified yet.`
}

function redisDetail(input: StackInput): string {
  const base = "Outbox stream and cache."
  if (input.redis === "external") return `${base} ${externalSentence}`
  if (input.redis === "resource")
    return `${base} Runs as ${input.redisResource ?? "its resource"} in this project.`
  return `${base} AOF every second.`
}

function backupSchedule(input: StackInput): string {
  if (input.backupPolicyStatus === "loading") return "Checking backup schedule"
  if (input.backupPolicyStatus === "denied")
    return "Backup schedule access denied"
  if (input.backupPolicyStatus === "unavailable")
    return "Backup schedule unavailable"
  const policy = input.backupPolicy
  if (!policy) return "Backups available"
  if (!policy.enabled) return "Backups paused"
  if (policy.lastError) return "Backup schedule needs attention"
  return policy.everyHours === 24
    ? "Backups daily"
    : `Backups every ${policy.everyHours} hours`
}

/**
 * stackParts is the cards of the Stack section, in the order the page
 * draws them: the engines, the one-shot migration, the server and console,
 * then the projections, which stay on the page as dashed cards when off.
 */
export function stackParts(input: StackInput): StackPart[] {
  const state = (id: StackPart["id"], running: StackState): StackState =>
    input.linked?.[id] ?? (input.down?.includes(id) ? "Not running" : running)
  const entityTypes =
    input.entityTypes === undefined
      ? ""
      : ` ${input.entityTypes} dynamic entity ${input.entityTypes === 1 ? "type" : "types"} loaded.`
  const parts: StackPart[] = [
    {
      id: "postgres",
      name: "Postgres 16",
      state: state("postgres", "Running"),
      detail: postgresDetail(input),
    },
    {
      id: "redis",
      name: "Redis",
      state: state("redis", "Running"),
      detail: redisDetail(input),
    },
    {
      id: "migrations",
      name: "Migrations",
      state: state("migrations", "Completed"),
      detail:
        input.migrations === undefined
          ? "Applied by fabriq migrate up."
          : `${input.migrations} applied by fabriq migrate up.`,
    },
    {
      id: "server",
      name: "Fabriq server",
      state: state("server", "Running"),
      detail: `Port 8081.${entityTypes}`,
    },
    {
      id: "admin",
      name: "Admin console",
      state: state("admin", "Running"),
      detail: "Opens through Loomplane, already signed in.",
    },
    {
      id: "falkordb",
      name: "FalkorDB",
      state: input.falkordb ? state("falkordb", "Running") : "Off",
      detail: input.falkordb
        ? "Graph projections. Rebuilt from Postgres, so no backups."
        : "Graph projections. Turn on in Settings; rebuilt from Postgres.",
    },
    {
      id: "elasticsearch",
      name: "Elasticsearch",
      state: input.elasticsearch ? state("elasticsearch", "Running") : "Off",
      detail: input.elasticsearch
        ? "Search projections. Rebuilt from Postgres, so no backups."
        : "Search projections. Turn on in Settings; rebuilt from Postgres.",
    },
  ]
  if (!input.library) return parts
  return parts
    .filter((part) => part.id !== "admin")
    .map((part) =>
      part.id === "server"
        ? {
            id: "worker",
            name: "Fabriq worker",
            state: state("worker", "Running"),
            detail: `Stock worker.${entityTypes}`,
          }
        : part
    )
}

/** A service of the manifest that needs the Fabriq, and the tenant it defaults to. */
export type FabriqNeed = { service: string; tenant?: string }

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/**
 * fabriqNeeds lists the services in a manifest that need a Fabriq resource,
 * by name, with the default tenant each passes it in
 * need_options.<resource>.tenant.
 */
export function fabriqNeeds(
  services: Record<string, unknown> | undefined,
  resource: string
): FabriqNeed[] {
  return Object.entries(services ?? {})
    .flatMap(([service, value]) => {
      if (!isRecord(value) || !Array.isArray(value.needs)) return []
      if (!value.needs.includes(resource)) return []
      const options = isRecord(value.need_options)
        ? value.need_options[resource]
        : undefined
      const tenant = isRecord(options) ? options.tenant : undefined
      return [
        { service, tenant: typeof tenant === "string" ? tenant : undefined },
      ]
    })
    .sort((a, b) => a.service.localeCompare(b.service))
}

/** unlinkedServices are the manifest's services that don't yet need the resource. */
export function unlinkedServices(
  services: Record<string, unknown> | undefined,
  resource: string
): string[] {
  const linked = new Set(fabriqNeeds(services, resource).map((n) => n.service))
  return Object.keys(services ?? {})
    .filter((service) => !linked.has(service))
    .sort((a, b) => a.localeCompare(b))
}
