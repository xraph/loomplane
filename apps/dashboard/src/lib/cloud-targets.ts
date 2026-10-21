import type {
  Environment,
  Manifest,
  Sync,
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

/** Adopt only the selected environment and preserve effective placement elsewhere. */
export function adoptServiceTarget(
  manifest: Manifest,
  environments: Environment[],
  environment: string,
  service: string,
  target: string
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
  for (const env of environments) {
    const settings = placementSettings(
      manifest,
      { section: "services", name: service, kind: "", entry: previous },
      env.name
    )
    const override = { ...entry.environments[env.name] }
    if (env.name === environment) {
      delete override.provider
      delete override.target
      override.cloud_target = target
    } else {
      if (settings.provider) override.provider = settings.provider
      if (settings.target) override.target = settings.target
      if (settings.region) override.region = settings.region
      if (settings.cloudTarget) override.cloud_target = settings.cloudTarget
    }
    entry.environments[env.name] = override
  }
  delete entry.provider
  delete entry.target
  delete entry.cloud_target
  delete entry.cloudTarget
  return next
}
