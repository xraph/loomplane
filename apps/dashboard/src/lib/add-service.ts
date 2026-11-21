// Adding a service to a project: from code analysis found, from your own
// source path, or from the catalog. Each add is one manifest edit that
// writes the entry, scopes it to an environment and wires it to what needs
// it, so the YAML stays as someone would write it by hand and the plan
// that follows deploys exactly that.
import { messageOf } from "@/client/errors"
import {
  useManifestsEdit as editManifest,
  usePlansCreate as createPlan,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  EditInputBody,
  KindInfo,
  Manifest,
  Plan,
  Provider,
  Revision,
} from "@/client/forge/loomplane/types"
import { targetsFor } from "@/lib/catalog-add"
import { enabledIn } from "@/lib/project-services"

/** The four things a service from your code can be. */
export const serviceKinds = [
  { value: "http-service", label: "HTTP service" },
  { value: "web-app", label: "Web app" },
  { value: "static-site", label: "Static site" },
  { value: "worker", label: "Worker" },
] as const

const nameRE = /^[a-z][a-z0-9-]{0,39}$/

/**
 * nameProblem says what's wrong with a name for a new manifest entry, or
 * "" when it's fine. An empty name is no problem yet: the form just
 * can't be sent.
 */
export function nameProblem(name: string, taken: Set<string>): string {
  const n = name.trim()
  if (!n) return ""
  if (taken.has(n)) return `The manifest already has something called ${n}.`
  if (!nameRE.test(n) || n.includes("--") || n.endsWith("-"))
    return "Use lower case letters, digits and single hyphens, starting with a letter, at most 40 long."
  return ""
}

/** slug turns any text into a manifest name. */
export function slug(text: string): string {
  let n = text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
  if (!n || !/^[a-z]/.test(n)) n = `service-${n}`.replace(/-$/, "")
  return n.slice(0, 40).replace(/-+$/, "")
}

/**
 * nameFromSource is what a source is named for: its last directory, its
 * Procfile process, or the directory its Dockerfile is in.
 */
export function nameFromSource(source: string): string {
  let s = source.trim()
  const colon = s.lastIndexOf(":")
  if (colon >= 0) s = s.slice(colon + 1)
  const parts = s.split("/").filter((p) => p && p !== ".")
  let last = parts.at(-1) ?? ""
  if (/^dockerfile/i.test(last) || /\.dockerfile$/i.test(last))
    last = parts.at(-2) ?? "app"
  return last ? slug(last) : ""
}

/**
 * envVarFor is the variable a service gets for something it needs, the
 * way the planner names it: a resource's by its kind, another service's
 * by its name. Two needs that would share a name each get their own
 * name's instead, which this doesn't predict; the plan says.
 */
export function envVarFor(
  need: { name: string; kind?: string },
  section: "services" | "resources"
): string {
  if (section === "services") return `${upper(need.name)}_URL`
  switch (need.kind) {
    case "postgres":
    case "mysql":
    case "mariadb":
      return "DATABASE_URL"
    case "redis":
    case "valkey":
      return "REDIS_URL"
    case "memcached":
      return "MEMCACHED_SERVERS"
    case "mongodb":
      return "MONGODB_URI"
    case "rabbitmq":
      return "AMQP_URL"
    case "kafka":
      return "KAFKA_BROKERS"
    case "nats":
      return "NATS_URL"
    case "object-storage":
      return "STORAGE_URL"
    case "search":
      return "SEARCH_URL"
    case "clickhouse":
      return "CLICKHOUSE_URL"
  }
  return `${upper(need.name)}_URL`
}

function upper(name: string): string {
  return name.toUpperCase().replace(/-/g, "_")
}

/** An entry in the manifest a new service can need, or be needed by. */
export type Entry = {
  name: string
  section: "services" | "resources"
  kind?: string
}

/** entries lists the manifest's services and resources, by name. */
export function entries(manifest: Manifest | undefined): Entry[] {
  const out: Entry[] = []
  for (const [name, s] of Object.entries(manifest?.services ?? {}))
    out.push({
      name,
      section: "services",
      kind: typeof s?.kind === "string" ? s.kind : undefined,
    })
  for (const [name, r] of Object.entries(manifest?.resources ?? {}))
    out.push({
      name,
      section: "resources",
      kind: typeof r?.kind === "string" ? r.kind : undefined,
    })
  return out.sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * scopeConflicts names each service that would run somewhere what it
 * needs doesn't, if the new entry is added as described: the same check
 * the server refuses an add on, so the form can say so first.
 */
export function scopeConflicts(
  manifest: Manifest | undefined,
  environments: string[],
  add: {
    name: string
    environment: string
    only: boolean
    needs?: string[]
    neededBy?: string[]
  }
): string[] {
  if (!add.only) return []
  const on = (name: string, env: string) => {
    if (name === add.name) return env === add.environment
    const entry = manifest?.services?.[name] ?? manifest?.resources?.[name]
    return enabledIn(entry, env)
  }
  const out: string[] = []
  for (const env of environments) {
    if (env === add.environment) continue
    for (const user of add.neededBy ?? [])
      if (on(user, env))
        out.push(
          `${user} runs in ${env} and would need ${add.name}, which would be off there.`
        )
  }
  // The new entry's own needs only matter where it runs, and it runs in
  // its environment alone.
  for (const need of add.needs ?? [])
    if (!on(need, add.environment))
      out.push(`${add.name} needs ${need}, which is off in ${add.environment}.`)
  return out
}

/** Where a catalog kind can run: a checked target, or a managed provider. */
export type Placement = {
  key: string
  provider: string
  /** The checked connection, for a server or a cluster. */
  target?: string
  cloud: string
  label: string
  detail: string
}

/**
 * placementsFor lists where a kind can run in a project: each checked
 * server or cluster that runs it, then each available provider that
 * provisions it, such as Cloud SQL for Postgres.
 */
export function placementsFor(
  kind: Pick<KindInfo, "name" | "targets">,
  connections: Connection[],
  providers: Provider[]
): Placement[] {
  const own = targetsFor(kind, connections).map(
    ({ connection: c, provider }): Placement => ({
      key: `${provider}:${c.id}`,
      provider,
      target: c.id,
      cloud: c.cloud,
      label: c.name,
      detail:
        provider === "kubernetes"
          ? `Kubernetes · ${c.details.namespace}`
          : "Docker on a server",
    })
  )
  const managed = providers
    .filter(
      (p) =>
        p.available &&
        p.name !== "server" &&
        p.name !== "kubernetes" &&
        (p.provisions ?? []).some((x) => x.kind === kind.name && !x.importOnly)
    )
    .map((p): Placement => ({
      key: p.name,
      provider: p.name,
      cloud: p.cloud,
      label: p.title,
      detail: "Managed by your cloud",
    }))
  return [...own, ...managed]
}

/** runnersOf lists the providers that run a service of kind, or any. */
export function runnersOf(providers: Provider[], kind?: string): Provider[] {
  return providers.filter(
    (p) =>
      p.available &&
      (p.runs ?? []).length > 0 &&
      (!kind || (p.runs ?? []).some((r) => r.workload === kind))
  )
}

export type AddEdit = Pick<
  EditInputBody,
  "addService" | "addResource" | "dryRun"
>

/** preview is the revision an add would save, saving nothing. */
export async function preview(
  projectId: string,
  edit: AddEdit
): Promise<Revision> {
  return editManifest({
    path: { id: projectId },
    body: { ...edit, dryRun: true },
  })
}

/**
 * addService saves the add as a new revision, then plans environment
 * unless plan is false. A refused edit stops before anything is planned.
 */
export async function addService(
  input: {
    projectId: string
    name: string
    environment: string
    edit: AddEdit
    plan: boolean
  },
  progress: (step: string) => void = () => {}
): Promise<{ revision: Revision; planId?: string }> {
  progress(`Adding ${input.name} to loomplane.yaml…`)
  const revision = await editManifest({
    path: { id: input.projectId },
    body: { ...input.edit, dryRun: undefined },
  })
  if (!input.plan) return { revision }
  progress(`Planning ${input.environment}…`)
  const m = revision.manifest
  const catalogOnly =
    m?.version === 1 &&
    [m.services, m.gateways, m.links].every(
      (value) => !value || Object.keys(value).length === 0
    )
  let plan: Plan
  try {
    plan = await createPlan({
      path: { id: input.projectId },
      body: {
        environment: input.environment,
        ...(catalogOnly ? { catalog: true } : {}),
      },
    })
  } catch (cause) {
    // The manifest has it now; adding again would only be refused for
    // the name. Planning from the project picks it up.
    throw new Error(
      `${input.name} is in loomplane.yaml now, but planning ${input.environment} didn't start: ${messageOf(cause, "the request failed.")} Plan it from the Deployments tab.`,
      { cause }
    )
  }
  return { revision, planId: plan.id }
}
