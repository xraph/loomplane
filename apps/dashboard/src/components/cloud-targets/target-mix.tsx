// A Cloud can spread its targets over any number of connected accounts,
// several of one provider among them. These show that spread: which
// accounts a Cloud's targets point at, grouped by provider.
import type { Connection, Target } from "@/client/forge/loomplane/types"
import { CloudGlyph } from "@/components/cloud-glyph"
import { cloudLabel } from "@/lib/clouds"
import { cn } from "@/lib/utils"

type Group = {
  cloud: string
  accounts: Set<string>
  targets: number
}

/** cloudOf is the provider a target runs on, from its account when known. */
export function cloudOf(t: Target, connections: Connection[]): string {
  return connections.find((c) => c.id === t.connectionId)?.cloud ?? t.provider
}

/** groups folds targets by provider, counting distinct accounts in each. */
export function groups(targets: Target[], connections: Connection[]) {
  const byCloud = new Map<string, Group>()
  for (const t of targets) {
    const cloud = cloudOf(t, connections)
    const g = byCloud.get(cloud) ?? { cloud, accounts: new Set(), targets: 0 }
    g.accounts.add(t.connectionId)
    g.targets++
    byCloud.set(cloud, g)
  }
  return [...byCloud.values()].sort(
    (a, b) => b.targets - a.targets || a.cloud.localeCompare(b.cloud)
  )
}

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

/** AccountSummary is one chip per provider: how many accounts and targets. */
export function AccountSummary({
  targets,
  connections,
  className,
}: {
  targets: Target[]
  connections: Connection[]
  className?: string
}) {
  const managed = targets.filter((t) => t.runtime !== "server")
  const pools = targets.filter((t) => t.runtime === "server")
  const list = groups(managed, connections)
  if (targets.length === 0) return null
  const accounts = new Set(managed.map((t) => t.connectionId)).size
  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      {managed.length > 0 && (
        <span className="text-[12.5px] text-muted-foreground">
          {plural(managed.length, "target")} across{" "}
          {plural(accounts, "account")} on {plural(list.length, "provider")}
        </span>
      )}
      {pools.length > 0 && (
        <span className="text-[12.5px] text-muted-foreground">
          {plural(pools.length, "server pool target")}
        </span>
      )}
      {list.map((g) => (
        <span
          key={g.cloud}
          className="inline-flex h-7 items-center gap-2 rounded-[9px] bg-card pr-2.5 pl-1 text-[12.5px] ring-1 ring-border"
        >
          <CloudGlyph cloud={g.cloud} size="xs" />
          <span className="font-medium">{cloudLabel(g.cloud)}</span>
          <span className="text-muted-foreground">
            {plural(g.accounts.size, "account")}
          </span>
        </span>
      ))}
    </div>
  )
}

/** AccountStack is the compact form for a list row: logos, then counts. */
export function AccountStack({
  targets,
  connections,
}: {
  targets: Target[]
  connections: Connection[]
}) {
  if (targets.length === 0)
    return <span className="text-muted-foreground">No targets</span>
  const accounts = [...new Set(targets.map((t) => t.connectionId))]
  const shown = accounts.slice(0, 4)
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      <span className="flex shrink-0 -space-x-1.5">
        {shown.map((id) => {
          const t = targets.find((x) => x.connectionId === id)!
          return (
            <span key={id} className="rounded-lg ring-2 ring-card">
              <CloudGlyph cloud={cloudOf(t, connections)} size="sm" />
            </span>
          )
        })}
        {accounts.length > shown.length && (
          <span className="flex size-7 items-center justify-center rounded-lg bg-muted font-mono text-[10.5px] ring-2 ring-card">
            +{accounts.length - shown.length}
          </span>
        )}
      </span>
      <span className="truncate text-[12.5px] text-muted-foreground">
        {plural(targets.length, "target")} ·{" "}
        {plural(accounts.length, "account")}
      </span>
    </span>
  )
}
