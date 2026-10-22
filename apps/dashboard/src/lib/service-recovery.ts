import type { Backup, Binding, Restore } from "@/client/forge/loomplane/types"

import { isDatabaseKind } from "@/lib/catalog-recipes"

/**
 * Protection is what recovery does for a resource's data, as the API's
 * recovery.Protection says it: "" is a database with no backup adapter on
 * its target.
 */
export type Protection = "native" | "none" | "rebuildable" | "external" | ""

/** Where one dependency of a composed resource comes from. */
export type DependencyChoice = {
  mode: "new" | "resource" | "external" | "off"
  resource?: string
  external?: string
  kind?: string
}

export type ProtectionView = { protection: Protection; label: string }

const protectionLabels: Record<Protection, string> = {
  native: "Native backups and test restores",
  none: "Not backed up, nothing to restore",
  rebuildable: "Rebuilt from its source, not backed up",
  external: "Not managed by Loomplane: no backups or restores",
  "": "No Loomplane backups on this target",
}

function view(protection: Protection): ProtectionView {
  return { protection, label: protectionLabels[protection] }
}

/** bindingConfig is a binding's config as an object, whether it arrived as JSON text or already parsed. */
export function bindingConfig(
  binding: Binding
): Record<string, unknown> | undefined {
  try {
    const config: unknown =
      typeof binding.config === "string"
        ? JSON.parse(binding.config)
        : binding.config
    return typeof config === "object" &&
      config !== null &&
      !Array.isArray(config)
      ? (config as Record<string, unknown>)
      : undefined
  } catch {
    return undefined
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null
}

/**
 * protectionOf says what recovery keeps of a binding, the same way the API
 * decides it. A component keeps nothing unless it's rebuilt, a composed
 * resource's parent keeps nothing, and a database is native where recovery
 * supports its engine on that target. An external dependency has no
 * binding, so its choice says it. Undefined means there is nothing to say:
 * the dependency is off, or what it points at isn't deployed.
 */
export function protectionOf(
  binding: Binding | undefined,
  choice?: DependencyChoice
): ProtectionView | undefined {
  if (choice?.mode === "external") return view("external")
  if (choice?.mode === "off" || !binding) return undefined
  const config = bindingConfig(binding)
  if (!config) return undefined
  if (config.provider === "external" || binding.provider === "external")
    return view("external")
  if (isObject(config.component))
    return view(
      config.component.protection === "rebuildable" ? "rebuildable" : "none"
    )
  if (isObject(config.composition)) return view("none")
  if (
    isDatabaseKind(config.kind) &&
    isObject(config.catalog) &&
    (binding.provider === "server" ||
      (binding.provider === "kubernetes" && config.kind === "postgres"))
  )
    return view("native")
  return view("")
}

export function recoverySelection(
  bindings: Binding[],
  backups: Backup[],
  restores: Restore[],
  environment: string | undefined,
  chosen: string
) {
  const deployed = bindings.filter(
    (binding) =>
      binding.subject === "resource" &&
      ["server", "kubernetes"].includes(binding.provider) &&
      binding.environmentId === environment &&
      protectionOf(binding)?.protection === "native"
  )
  const history = backups.filter(
    (backup) => backup.environmentId === environment
  )
  const resources = [
    ...new Set([
      ...deployed.map((binding) => binding.name),
      ...history.map((backup) => backup.name),
    ]),
  ]
  const selected = resources.includes(chosen) ? chosen : (resources[0] ?? "")
  const points = history.filter((backup) => backup.name === selected)
  const ids = new Set(points.map((backup) => backup.id))
  return {
    resources,
    selected,
    live: deployed.some((binding) => binding.name === selected),
    points,
    copies: restores.filter((copy) => ids.has(copy.backupId)),
  }
}
