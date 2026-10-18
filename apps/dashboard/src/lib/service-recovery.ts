import type { Backup, Binding, Restore } from "@/client/forge/loomplane/types"

export function recoverySelection(
  bindings: Binding[],
  backups: Backup[],
  restores: Restore[],
  environment: string | undefined,
  chosen: string
) {
  const deployed = bindings.filter((binding) => {
    if (
      binding.subject !== "resource" ||
      binding.provider !== "server" ||
      binding.environmentId !== environment
    )
      return false
    try {
      const config: unknown =
        typeof binding.config === "string"
          ? JSON.parse(binding.config)
          : binding.config
      return (
        typeof config === "object" &&
        config !== null &&
        "kind" in config &&
        config.kind === "postgres" &&
        "catalog" in config &&
        typeof config.catalog === "object" &&
        config.catalog !== null
      )
    } catch {
      return false
    }
  })
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
