import { statusOf } from "@/client/errors"
import {
  useBackupPoliciesList,
  useFabriqStatus,
  useServiceBackupsList,
  useServiceRestoresList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import {
  componentName,
  dependencyChoices,
  type CatalogPanelProps,
} from "@/lib/catalog-kinds"
import { stackParts, type StackInput, type StackPart } from "@/lib/fabriq"
import { useNow } from "@/lib/use-runs"

export function useFabriqStack({
  projectId,
  environmentId,
  resource,
  binding,
  config,
  kind,
  components,
}: CatalogPanelProps) {
  const now = useNow(30_000)
  const status = useQuery(useFabriqStatus, {
    path: { projectId, environmentId, resource },
  })
  const policies = useQuery(useBackupPoliciesList, { path: { id: projectId } })
  const backups = useQuery(useServiceBackupsList, { path: { id: projectId } })
  const restores = useQuery(useServiceRestoresList, { path: { id: projectId } })

  const choices = Object.fromEntries(dependencyChoices(config))
  const deployed = new Map(components.map((b) => [b.name, b]))
  // A part is down when its binding is missing or was deleted outside
  // Loomplane. A dependency that isn't provisioned with Fabriq has no
  // binding here, and isn't judged.
  const linked: StackInput["linked"] = {}
  const down: StackPart["id"][] = []
  const check = (id: StackPart["id"], name?: string) => {
    if (!name) return
    const b = deployed.get(name)
    if (!b || b.drift === "missing") down.push(id)
  }
  const processes =
    kind === "fabriq-library"
      ? (["migrate", "worker"] as const)
      : (["migrate", "server", "admin"] as const)
  for (const part of processes)
    check(
      part === "migrate" ? "migrations" : part,
      componentName(resource, part)
    )
  for (const id of [
    "postgres",
    "redis",
    "falkordb",
    "elasticsearch",
  ] as const) {
    if (choices[id]?.mode === "new") check(id, choices[id].resource)
    else if (choices[id]?.mode === "external") linked[id] = "External"
    else if (choices[id]?.mode === "resource") linked[id] = "Linked"
  }

  const postgres = choices.postgres
  const postgresResource =
    postgres?.mode === "new" || postgres?.mode === "resource"
      ? postgres.resource
      : undefined
  const reported = status.data?.reachable ? status.data : undefined
  const input: StackInput = {
    library: kind === "fabriq-library",
    provider: binding.provider,
    postgres: postgres?.mode === "new" ? "managed" : "linked",
    redis: choices.redis?.mode === "new" ? "managed" : "linked",
    backupPolicy: policies.data?.policies?.find(
      (policy) =>
        policy.environmentId === environmentId &&
        policy.resource === postgresResource
    ),
    backupPolicyStatus: policies.error
      ? statusOf(policies.error) === 403
        ? "denied"
        : "unavailable"
      : !policies.data
        ? "loading"
        : undefined,
    restoreStatus:
      backups.error || restores.error
        ? "unavailable"
        : !backups.data || !restores.data
          ? "loading"
          : undefined,
    restoreVerifiedAt: lastVerified(
      backups.data?.backups ?? [],
      restores.data?.restores ?? [],
      environmentId,
      postgresResource
    ),
    now,
    down,
    linked,
    falkordb: !!choices.falkordb && choices.falkordb.mode !== "off",
    elasticsearch:
      !!choices.elasticsearch && choices.elasticsearch.mode !== "off",
    migrations: reported?.migrationsKnown ? reported.migrations : undefined,
    // A count of none is also what Fabriq answers when it doesn't report one.
    entityTypes:
      reported && reported.entityTypes > 0 ? reported.entityTypes : undefined,
  }
  const migrated = deployed.get(componentName(resource, "migrate"))
  return { parts: stackParts(input), migrated, now }
}

// The newest verified test restore of any backup of the database, in this
// environment.
function lastVerified(
  backups: { id: string; name: string; environmentId: string }[],
  restores: { backupId: string; verifiedAt?: string }[],
  environmentId: string,
  resource?: string
): string | undefined {
  if (!resource) return undefined
  const ids = new Set(
    backups
      .filter((b) => b.name === resource && b.environmentId === environmentId)
      .map((b) => b.id)
  )
  return restores
    .filter((r) => ids.has(r.backupId) && r.verifiedAt)
    .map((r) => r.verifiedAt!)
    .sort()
    .at(-1)
}
