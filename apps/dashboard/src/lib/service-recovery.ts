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

/** protectionLabel says, in a sentence, what recovery keeps under a protection. */
export function protectionLabel(protection: Protection): string {
  return protectionLabels[protection]
}

function view(protection: Protection): ProtectionView {
  return { protection, label: protectionLabels[protection] }
}

/** isRecord reports whether a value is a JSON object: not null, not an array. */
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
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
    return isRecord(config) ? config : undefined
  } catch {
    return undefined
  }
}

/**
 * protectionOf says what recovery keeps of a binding. It follows the API's
 * recovery.Protection (a component keeps nothing unless it's rebuilt, a
 * composed resource's parent keeps nothing, a database is native where
 * recovery supports it on that target), with these differences:
 *
 *   - native is decided for the five built-in engine kinds only. The API
 *     also maps engine families, such as an engine registered as postgres
 *     under another name, which this doesn't know about.
 *   - native reads the binding's provider, as recoverySelection always has,
 *     not the provider in its config.
 *   - an external dependency has no binding, so its choice stands in for
 *     the API's synthetic external config.
 *   - it returns undefined where the API has nothing to decide: the
 *     dependency is off, or what it points at isn't deployed.
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
  if (isRecord(config.component))
    return view(
      config.component.protection === "rebuildable" ? "rebuildable" : "none"
    )
  if (isRecord(config.composition)) return view("none")
  if (
    isDatabaseKind(config.kind) &&
    isRecord(config.catalog) &&
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
