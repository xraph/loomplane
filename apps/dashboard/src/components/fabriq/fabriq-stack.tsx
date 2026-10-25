// The Stack section of a hosted Fabriq's Tenants and keys tab: what the
// one click set up, each part with where it stands.
import type { ReactNode } from "react"

import { statusOf } from "@/client/errors"
import {
  useBackupPoliciesList,
  useFabriqStatus,
  useServiceBackupsList,
  useServiceRestoresList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { toneClass, type Tone } from "@/components/runs"
import {
  componentName,
  dependencyChoices,
  type CatalogPanelProps,
} from "@/lib/catalog-kinds"
import {
  ago,
  stackParts,
  type StackInput,
  type StackPart,
  type StackState,
} from "@/lib/fabriq"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

const tones: Record<StackState, Tone> = {
  Running: "live",
  Completed: "quiet",
  Off: "quiet",
  "Not running": "waiting",
}

/** StatePill is a part's state in its tone: green running, grey done or off. */
export function StatePill({
  tone,
  title,
  children,
}: {
  tone: Tone
  title?: string
  children: ReactNode
}) {
  const t = toneClass[tone]
  return (
    <span
      title={title}
      className={cn(
        "inline-flex h-[19px] shrink-0 items-center rounded-full px-[7px] text-[11.5px] font-medium whitespace-nowrap",
        t.tint,
        t.ink
      )}
    >
      {children}
    </span>
  )
}

/**
 * FabriqStack draws the stack from the resource's bindings, the status
 * Fabriq reports when it answers, and the test restores of its Postgres.
 * It doesn't need Fabriq to be reachable.
 */
export function FabriqStack({
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
  for (const id of ["postgres", "redis", "falkordb", "elasticsearch"] as const)
    if (choices[id]?.mode === "new") check(id, choices[id].resource)

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
    restoreVerifiedAt: lastVerified(
      backups.data?.backups ?? [],
      restores.data?.restores ?? [],
      environmentId,
      postgresResource
    ),
    now,
    down,
    falkordb: !!choices.falkordb && choices.falkordb.mode !== "off",
    elasticsearch:
      !!choices.elasticsearch && choices.elasticsearch.mode !== "off",
    migrations: reported?.migrationsKnown ? reported.migrations : undefined,
    // A count of none is also what Fabriq answers when it doesn't report one.
    entityTypes:
      reported && reported.entityTypes > 0 ? reported.entityTypes : undefined,
  }
  const migrated = deployed.get(componentName(resource, "migrate"))
  return (
    <section aria-label="Stack" className="grid gap-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <h2 className="text-[15px] font-semibold">Stack</h2>
        <span className="text-[12.5px] text-muted-foreground">
          Set up in one click
          {migrated && ` · migrations applied ${ago(migrated.updatedAt, now)}`}
        </span>
      </div>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-2">
        {stackParts(input).map((part) => (
          <div
            key={part.id}
            className={cn(
              "grid content-start gap-1 rounded-[12px] border px-3 py-2.5",
              part.state === "Off" ? "border-dashed bg-subtle" : "bg-card"
            )}
          >
            <span className="flex items-center justify-between gap-1.5">
              <span className="text-[13px] font-semibold">{part.name}</span>
              <StatePill tone={tones[part.state]}>{part.state}</StatePill>
            </span>
            <span className="text-xs leading-[1.45] text-muted-foreground">
              {part.detail}
            </span>
          </div>
        ))}
      </div>
    </section>
  )
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
