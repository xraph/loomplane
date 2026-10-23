// Adding a catalog service to a project that already exists: the resource
// goes into the project's manifest, then Loomplane plans one environment.
// Nothing is approved here; the plan opens on its review page like any
// other.
import { messageOf } from "@/client/errors"
import {
  useManifestsEdit as editManifest,
  usePlansCreate as createPlan,
} from "@/client/forge/loomplane/hooks"
import type { KindInfo, Plan } from "@/client/forge/loomplane/types"
import type { DependencyChoice } from "@/lib/service-recovery"

export type AddCatalogServiceInput = {
  projectId: string
  /** The environment's name, such as "production". */
  environment: string
  /** The resource's name in the manifest. */
  name: string
  kind: string
  provider: "server" | "kubernetes"
  /** The checked connection it runs on. */
  target: string
  version?: string
  uses?: Record<
    string,
    { resource?: string; external?: string; enabled?: boolean }
  >
}

/**
 * addCatalogService adds the resource to the project's manifest, then plans
 * the environment. A refused edit, such as a name the manifest already
 * uses, stops before anything is planned.
 */
export async function addCatalogService(
  input: AddCatalogServiceInput,
  progress: (step: string) => void
): Promise<{ planId: string }> {
  progress(`Adding ${input.name} to the manifest…`)
  const revision = await editManifest({
    path: { id: input.projectId },
    body: {
      addResource: {
        name: input.name,
        kind: input.kind,
        provider: input.provider,
        target: input.target,
        ...(input.version ? { version: input.version } : {}),
        ...(input.uses && Object.keys(input.uses).length
          ? { uses: input.uses }
          : {}),
      },
    },
  })
  progress(`Planning ${input.environment}…`)
  const manifest = revision.manifest
  const resourceOnly =
    manifest?.version === 1 &&
    [manifest.services, manifest.gateways, manifest.links].every(
      (value) => !value || Object.keys(value).length === 0
    )
  let plan: Plan
  try {
    plan = await createPlan({
      path: { id: input.projectId },
      body: {
        environment: input.environment,
        ...(resourceOnly ? { catalog: true } : {}),
      },
    })
  } catch (cause) {
    // The manifest already has it, so trying again here would only be
    // refused for the name. Planning from the project picks it up.
    throw new Error(
      `${input.name} is in the manifest now, but the plan didn't start: ${messageOf(cause, "the request failed.")} Plan ${input.environment} from the project.`,
      { cause }
    )
  }
  return { planId: plan.id }
}

/** DependencyPicks is where each of a kind's dependencies comes from. */
export type DependencyPicks = Record<string, DependencyChoice>

/**
 * initialDependencyPicks provisions every dependency new, except an
 * optional one that's off unless the kind turns it on by default.
 */
export function initialDependencyPicks(kind: KindInfo): DependencyPicks {
  const picks: DependencyPicks = {}
  for (const dep of kind.dependencies ?? [])
    picks[dep.name] =
      dep.optional && !dep.default
        ? { mode: "off" }
        : { mode: "new", kind: dep.kinds[0] }
  return picks
}

/**
 * usesOf is the manifest's uses for the picks. It says only what differs
 * from the kind's defaults: a dependency left out is provisioned new when
 * it's required or on by default, and left off otherwise.
 */
export function usesOf(
  kind: KindInfo,
  picks: DependencyPicks
): AddCatalogServiceInput["uses"] {
  const uses: NonNullable<AddCatalogServiceInput["uses"]> = {}
  for (const dep of kind.dependencies ?? []) {
    const pick = picks[dep.name]
    const on = !dep.optional || dep.default
    if (pick?.mode === "resource" && pick.resource)
      uses[dep.name] = { resource: pick.resource }
    else if (pick?.mode === "external" && pick.external)
      uses[dep.name] = { external: pick.external }
    else if (pick?.mode === "off" && on && dep.optional)
      uses[dep.name] = { enabled: false }
    else if (pick?.mode === "new" && !on) uses[dep.name] = { enabled: true }
  }
  return Object.keys(uses).length ? uses : undefined
}
