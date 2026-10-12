// Environments and the layered variables set on them, made readable.
// Mirrors internal/environments: the API decides, this only explains.

import type { Variable } from "@/client/forge/loomplane/types"

export const environmentKinds = [
  {
    value: "production",
    label: "Production",
    description: "Serves your users.",
  },
  {
    value: "staging",
    label: "Staging",
    description: "A copy of production to try changes on first.",
  },
  {
    value: "preview",
    label: "Preview",
    description: "Short-lived deployments of branches and pull requests.",
  },
  {
    value: "development",
    label: "Development",
    description: "For building and testing as you go.",
  },
] as const

/** kindFor guesses an environment's kind from its name, as the API does. */
export function kindFor(name: string): string {
  return environmentKinds.some((k) => k.value === name) ? name : "development"
}

/** Where a value is set: the whole project, or one service. */
export interface Target {
  /** A service ID from the latest analysis, or "" for the whole project. */
  service: string
  serviceName?: string
}

const scopeLabels: Record<string, string> = {
  organization: "Organization",
  project: "Project",
  service: "Service",
}

/** layerLabel names the layer a variable is set on. */
export function layerLabel(v: Variable): string {
  return scopeLabels[v.scope] ?? v.scope
}

/** reachLabel says which environments a variable applies in. */
export function reachLabel(v: Variable): string {
  return v.environment ? `${v.environment} only` : "every environment"
}

/**
 * ownLayer reports whether v is set on the layer being looked at, so it's
 * edited here, rather than inherited and only overridden here.
 */
export function ownLayer(v: Variable, target: Target): boolean {
  if (target.service) {
    return v.scope === "service" && v.service === target.service
  }
  return v.scope === "project"
}

/** keyValid is the API's rule for a key: letters, digits, underscores. */
export function keyValid(key: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(key) && key.length <= 255
}
