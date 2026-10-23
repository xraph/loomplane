import { messageOf, statusOf } from "@/client/errors"
import type {
  Environment,
  Manifest,
  Sync,
  Target,
} from "@/client/forge/loomplane/types"
import { placementSettings } from "@/lib/placement"

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

/** Adopt only the selected environment and preserve effective placement elsewhere. */
export function adoptServiceTarget(
  manifest: Manifest,
  environments: Environment[],
  environment: string,
  service: string,
  choice: CloudChoice
): Manifest {
  const next = structuredClone(manifest)
  if (!next.services?.[service])
    throw new Error("This service no longer exists. Refresh the manifest.")
  // A default provider would also apply to a named target. Materialize it before
  // removing that inheritance, including resources and gateways.
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
    }
    // An environment with nothing to pin needs no override of its own.
    if (Object.keys(override).length || previous.environments?.[name])
      entry.environments[name] = override
  }
  delete entry.provider
  delete entry.target
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
    ? `This ${kind} is protected while environments, deployments, syncs or recent previews reference it. Preview references expire after 24 hours.`
    : messageOf(
        error,
        `The ${kind} couldn't be deleted. Remove its references first.`
      )
}
