import { messageOf, statusOf } from "@/client/errors"
import type {
  Environment,
  Manifest,
  Server,
  Sync,
  Target,
} from "@/client/forge/loomplane/types"
import { cloudLabel } from "@/lib/clouds"
import { placementSettings } from "@/lib/placement"

export function serverForConnection(
  servers: (Server | null)[] | undefined,
  connectionId: string | undefined
) {
  if (!connectionId) return undefined
  return servers?.find(
    (s): s is Server =>
      s?.runtimeConnectionId === connectionId &&
      s.status !== "deleted" &&
      s.status !== "disconnected"
  )
}

export function serverLocation(server: Server) {
  return [cloudLabel(server.provider), server.region]
    .filter(Boolean)
    .join(" · ")
}

export const activeSync = (sync: Sync) =>
  ["pending", "running", "reverting"].includes(sync.status)

export const syncLabel = (status: string) =>
  ({
    routing_pending: "Routing needs review",
    cleanup_pending: "Cleanup pending",
    succeeded: "In sync",
    reverted: "Reverted",
    pending: "Queued",
    running: "Syncing",
    reverting: "Reverting",
    failed: "Failed",
  })[status] ?? status

/** A service picks its Cloud target by name, or by tags a target carries. */
export type CloudChoice = string | { tags: string[] }

/**
 * TagMatch mirrors the planner: tags select the one granted target that
 * carries every tag. None or several fail the plan rather than guessing.
 */
export type TagMatch =
  | { kind: "one"; target: Target }
  | { kind: "none" }
  | { kind: "many"; targets: Target[] }

export function matchCloudTags(targets: Target[], tags: string[]): TagMatch {
  const found = targets.filter((t) =>
    tags.every((tag) => (t.tags ?? []).includes(tag))
  )
  if (found.length === 1) return { kind: "one", target: found[0] }
  if (found.length === 0) return { kind: "none" }
  return { kind: "many", targets: found }
}

/**
 * adoptServiceTarget puts a service on a Cloud target, or on the one its
 * tags select, through the project's default Cloud.
 *
 * Without `only`, it does so in every environment: the service's direct
 * placement goes, and so does any placement an environment set for it, so
 * every environment resolves the same way.
 *
 * With `only`, just that environment moves. A service's base placement can't
 * be unset by an override, so every other environment's effective placement
 * is written into its own override first and the base is cleared.
 */
export function adoptServiceTarget(
  manifest: Manifest,
  service: string,
  choice: CloudChoice,
  only?: { environment: string; environments: Environment[] },
  /** cloud names a Cloud other than the project's default. */
  cloud?: string
): Manifest {
  if (only)
    return adoptInEnvironment(
      manifest,
      only.environments,
      only.environment,
      service,
      choice,
      cloud
    )
  const next = structuredClone(manifest)
  const entry = next.services?.[service]
  if (!entry)
    throw new Error("This service no longer exists. Refresh the manifest.")
  // A default provider would also apply to a named target. Give every other
  // entry its own before the default goes.
  materializeDefaults(manifest, next)
  for (const key of placementKeys) delete entry[key]
  if (cloud) entry.cloud = cloud
  if (typeof choice === "string") entry.cloud_target = choice
  else entry.cloud_tags = [...choice.tags]
  for (const [name, override] of Object.entries(entry.environments ?? {})) {
    const rest: Record<string, unknown> = {
      ...(override as Record<string, unknown>),
    }
    for (const key of placementKeys) delete rest[key]
    if (Object.keys(rest).length) entry.environments![name] = rest
    else delete entry.environments![name]
  }
  if (entry.environments && !Object.keys(entry.environments).length)
    delete entry.environments
  return next
}

// placementKeys are the fields that say where a service runs, in both the
// wire and the client spelling a manifest may carry.
const placementKeys = [
  "cloud",
  "provider",
  "target",
  "cloud_target",
  "cloudTarget",
  "cloud_tags",
  "cloudTags",
] as const

function materializeDefaults(manifest: Manifest, next: Manifest) {
  for (const section of ["services", "resources", "gateways"] as const) {
    for (const [name, entry] of Object.entries(next[section] ?? {})) {
      const effective = placementSettings(
        manifest,
        { section, name, kind: "", entry },
        ""
      )
      if (!entry.provider && effective.provider)
        entry.provider = effective.provider
      if (!entry.region && effective.region) entry.region = effective.region
    }
  }
  if (next.defaults) {
    delete next.defaults.provider
    delete next.defaults.region
  }
}

function adoptInEnvironment(
  manifest: Manifest,
  environments: Environment[],
  environment: string,
  service: string,
  choice: CloudChoice,
  cloud?: string
): Manifest {
  const next = structuredClone(manifest)
  if (!next.services?.[service])
    throw new Error("This service no longer exists. Refresh the manifest.")
  materializeDefaults(manifest, next)
  const entry = next.services![service]
  const previous = manifest.services![service]
  entry.environments = { ...entry.environments }
  const names = new Set([
    ...environments.map((env) => env.name),
    ...Object.keys(previous.environments ?? {}),
  ])
  for (const name of names) {
    const settings = placementSettings(
      manifest,
      { section: "services", name: service, kind: "", entry: previous },
      name
    )
    const override = { ...entry.environments[name] }
    if (name === environment) {
      delete override.provider
      delete override.target
      delete override.cloudTarget
      delete override.cloudTags
      // An environment's target is in the Cloud it names, or the default.
      if (cloud) override.cloud = cloud
      else delete override.cloud
      if (typeof choice === "string") {
        override.cloud_target = choice
        delete override.cloud_tags
      } else {
        override.cloud_tags = [...choice.tags]
        delete override.cloud_target
      }
    } else {
      if (settings.provider) override.provider = settings.provider
      if (settings.target) override.target = settings.target
      if (settings.region) override.region = settings.region
      // Changing the base provider/target makes Service.For clear its inherited
      // endpoint. Pin the old effective endpoint explicitly in each other scope.
      if (settings.endpoint) {
        const endpoint = settings.endpoint
        override.endpoint = {
          ...(endpoint.hostname !== undefined
            ? { hostname: endpoint.hostname }
            : {}),
          ...(endpoint.hostPort !== undefined
            ? { host_port: endpoint.hostPort }
            : {}),
          ...(endpoint.ingressClass !== undefined
            ? { ingress_class: endpoint.ingressClass }
            : {}),
          ...(endpoint.tlsSecret !== undefined
            ? { tls_secret: endpoint.tlsSecret }
            : {}),
        }
      }
      if (settings.cloudTarget) override.cloud_target = settings.cloudTarget
      if (settings.cloudTags) override.cloud_tags = settings.cloudTags
      if (settings.cloud) override.cloud = settings.cloud
    }
    // An environment with nothing to pin needs no override of its own.
    if (Object.keys(override).length || previous.environments?.[name])
      entry.environments[name] = override
  }
  delete entry.provider
  delete entry.target
  delete entry.cloud
  delete entry.cloud_target
  delete entry.cloudTarget
  delete entry.cloud_tags
  delete entry.cloudTags
  return next
}

export function cloudDeletionError(
  error: unknown,
  kind: "Cloud" | "target"
): string {
  return statusOf(error) === 409
    ? `This ${kind} is protected while projects, deployments, syncs or recent previews use it. Preview references expire after 24 hours.`
    : messageOf(
        error,
        `The ${kind} couldn't be deleted. Remove its references first.`
      )
}
