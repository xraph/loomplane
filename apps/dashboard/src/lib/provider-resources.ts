import { messageOf } from "@/client/errors"
import {
  useManifestsEdit as editManifest,
  usePlansCreate as createPlan,
} from "@/client/forge/loomplane/hooks"
import type { Connection, Provider } from "@/client/forge/loomplane/types"

export type InventoryConsumer = {
  id: string
  bindingId: string
  projectId: string
  environmentId: string
  name: string
}
export type InventoryResource = {
  id: string
  provider: string
  connectionId: string
  connectionIdentity?: string
  accountIdentity: string
  externalId: string
  name: string
  kind: string
  region: string
  status: string
  ownership: string
  endpointId: string
  observedAt: string
  revision: number
  version?: string
  host?: string
  port?: number
  database?: string
  scope?: string
  observationState?: string
  connectionState?: string
  credentialMethods?: string[]
  consumers?: InventoryConsumer[]
  ownershipState?: string
  endpointProofRequired?: boolean
  identityScope?: string
  bucketOwner?: string
}
export type ResourceFilters = {
  search?: string
  ownership?: string
  connection?: string
  accountIdentity?: string
  provider?: string
  kind?: string
}
export function filterProviderResources(
  resources: InventoryResource[],
  filters: ResourceFilters
) {
  const search = filters.search?.trim().toLowerCase() ?? ""
  return resources.filter(
    (r) =>
      (!filters.ownership ||
        filters.ownership === "all" ||
        r.ownership === filters.ownership) &&
      (!filters.connection ||
        filters.connection === "all" ||
        r.connectionId === filters.connection ||
        (!!filters.accountIdentity &&
          (r.accountIdentity === filters.accountIdentity ||
            r.connectionIdentity === filters.accountIdentity) &&
          r.provider === filters.provider)) &&
      (!filters.kind || filters.kind === "all" || r.kind === filters.kind) &&
      (!search ||
        [
          r.name,
          r.kind,
          r.provider,
          r.region,
          r.accountIdentity,
          r.externalId,
        ].some((value) => value.toLowerCase().includes(search)))
  )
}
export function providerResourceReadiness(
  resource: InventoryResource,
  now: number
) {
  const age = now - new Date(resource.observedAt).getTime()
  return {
    discovery:
      resource.status === "missing" || resource.observationState === "missing"
        ? "Missing"
        : resource.observationState === "stale" ||
            !Number.isFinite(age) ||
            age > 30 * 60_000
          ? "Stale"
          : "Observed",
    credentials: resource.endpointId
      ? "Credentials saved"
      : "Credentials needed",
    network: "Verify in deployment",
  }
}
export function resourceOfferings(
  providers: Provider[],
  connections: Connection[]
) {
  return providers.flatMap((provider) =>
    !provider.available
      ? []
      : (provider.provisions ?? []).flatMap((offer) =>
          connections
            .filter(
              (connection) =>
                connection.status === "ready" &&
                connection.cloud === provider.cloud
            )
            .map((connection) => ({ provider, offer, connection }))
        )
  )
}
export type ResourceReviewInput = {
  projectId: string
  environment: string
  name: string
  kind: string
  existingResource?: string
  provider?: string
  target?: string
  version?: string
  region?: string
  settings?: Record<string, string>
  service?: string
}
export async function reviewProviderResource(
  input: ResourceReviewInput,
  progress: (step: string) => void
) {
  progress(`Adding ${input.name} to your manifest…`)
  const revision = await editManifest({
    path: { id: input.projectId },
    body: {
      addResource: {
        name: input.name,
        kind: input.kind,
        enabledIn: input.environment,
        ...(input.existingResource
          ? { existingResource: input.existingResource }
          : {
              provider: input.provider,
              target: input.target,
              version: input.version,
              region: input.region,
              settings: input.settings,
            }),
      },
    },
  })
  try {
    if (input.service)
      await editManifest({
        path: { id: input.projectId },
        body: { link: { service: input.service, need: input.name } },
      })
    progress(`Planning ${input.environment}…`)
    const manifest = revision.manifest
    const resourceOnly =
      manifest?.version === 1 &&
      [manifest.services, manifest.gateways, manifest.links].every(
        (section) => !section || Object.keys(section).length === 0
      )
    const plan = await createPlan({
      path: { id: input.projectId },
      body: {
        environment: input.environment,
        ...(resourceOnly ? { catalog: true } : {}),
      },
    })
    return { planId: plan.id }
  } catch (cause) {
    throw new Error(
      `${input.name} is in your manifest, but review couldn't start: ${messageOf(cause, "the request failed.")} Open your project to finish the review.`,
      { cause }
    )
  }
}
