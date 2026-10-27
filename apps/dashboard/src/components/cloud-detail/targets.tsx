// A Cloud's default and its targets: where each runs, and how many running
// services it placed. Editing reuses the target editor and its review.
import { useState } from "react"

import type {
  Connection,
  Pool,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import { CloudGlyph } from "@/components/cloud-glyph"
import {
  CloudEmpty,
  CloudSectionHead,
  listItemClass,
} from "@/components/cloud-targets/infrastructure-ui"
import { DefaultPlacementCard } from "@/components/cloud-targets/org-cloud"
import { TargetEditor } from "@/components/cloud-targets/target-editor"
import { cloudTags } from "@/components/cloud-targets/workspace"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Item, ItemGroup } from "@/components/ui/item"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

import { servicesPlaced, whereLabel, type CloudScope } from "./data"

const columns = "md:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_110px_72px]"

/** RunsOn is a target's account, linked to its page, and where it runs. */
export function RunsOn({
  target,
  connections,
  pools,
}: {
  target: Pick<
    Target,
    "runtime" | "poolId" | "provider" | "connectionId" | "region"
  >
  connections: Connection[]
  pools?: Pool[]
}) {
  if (target.runtime === "server") {
    const pool = pools?.find((p) => p.id === target.poolId)
    return (
      <span className="flex min-w-0 items-center gap-2.5">
        <CloudGlyph cloud="server" size="sm" />
        <span className="grid min-w-0 gap-0.5">
          <span className="truncate text-[13px]">
            {pool ? `Pool ${pool.name}` : "A server pool"}
          </span>
          <span className="truncate text-xs text-muted-foreground">
            Servers on this Cloud
          </span>
        </span>
      </span>
    )
  }
  const account = connections.find((c) => c.id === target.connectionId)
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      <CloudGlyph cloud={account?.cloud ?? target.provider} size="sm" />
      <span className="grid min-w-0 gap-0.5">
        {account ? (
          <Link
            to="/clouds/accounts/$id"
            params={{ id: account.id }}
            className="truncate text-[13px] font-medium hover:underline"
          >
            {account.name}
          </Link>
        ) : (
          <span className="truncate text-[13px]">A removed account</span>
        )}
        <span className="truncate text-xs text-muted-foreground">
          {whereLabel(connections, target)}
        </span>
      </span>
    </span>
  )
}

export function Targets({
  scope,
  targets,
  connections,
  pools,
  usage,
  manage,
  onSaved,
}: {
  scope: CloudScope
  targets: Target[]
  connections: Connection[]
  pools?: Pool[]
  usage?: Usage
  manage: boolean
  onSaved: () => void
}) {
  const [editing, setEditing] = useState<Target | "new" | null>(null)
  const placed = servicesPlaced(usage)
  return (
    <div className="grid gap-6">
      {!scope.project && (
        <DefaultPlacementCard cloud={scope.cloud} targets={targets} />
      )}
      <section aria-label="Targets" className="grid gap-3">
        <CloudSectionHead
          title="Targets"
          count={targets.length}
          description="Renaming or retagging saves at once. Moving a target to another account, region or pool opens a review, and nothing moves until you sync it."
        >
          {manage && (
            <Button onClick={() => setEditing("new")}>Add target</Button>
          )}
        </CloudSectionHead>
        {targets.length === 0 ? (
          <CloudEmpty
            title="No targets yet"
            action={
              manage && (
                <Button onClick={() => setEditing("new")}>Add target</Button>
              )
            }
          >
            {scope.project
              ? "This project may use the Cloud's default only."
              : "Add a target such as frontend or backend and pick the account it runs on. Without one, services use the default."}
          </CloudEmpty>
        ) : (
          <Card size="sm" className="gap-0 py-0">
            <div
              aria-hidden="true"
              className={cn(
                "grid gap-4 border-b bg-subtle px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase max-md:hidden",
                columns
              )}
            >
              <span>Target</span>
              <span>Runs on</span>
              <span>Services</span>
              <span />
            </div>
            <ItemGroup className="gap-0">
              {targets.map((t) => (
                <Item
                  role="listitem"
                  size="sm"
                  key={t.id}
                  className={cn(
                    listItemClass,
                    "grid items-center gap-x-4 gap-y-2 py-2.5",
                    columns
                  )}
                >
                  <span className="grid min-w-0 gap-1">
                    <span className="flex flex-wrap items-baseline gap-2">
                      <span className="truncate text-[13px] font-semibold">
                        {t.displayName || t.name}
                      </span>
                      <span className="truncate font-mono text-[11.5px] text-muted-foreground">
                        {t.name}
                      </span>
                    </span>
                    {(t.tags ?? []).length > 0 && (
                      <span className="flex flex-wrap gap-1">
                        {t.tags.map((tag) => (
                          <span
                            key={tag}
                            className="rounded-[5px] bg-muted px-1.5 font-mono text-[11px]"
                          >
                            {tag}
                          </span>
                        ))}
                      </span>
                    )}
                  </span>
                  <RunsOn target={t} connections={connections} pools={pools} />
                  <span className="text-[12.5px] text-muted-foreground">
                    {usage ? (
                      <>
                        <span className="font-mono text-foreground tabular-nums">
                          {placed.get(t.id) ?? 0}
                        </span>{" "}
                        running
                      </>
                    ) : (
                      "…"
                    )}
                  </span>
                  {manage && (
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Edit ${t.name}`}
                      className="justify-self-start md:justify-self-end"
                      onClick={() => setEditing(t)}
                    >
                      Edit
                    </Button>
                  )}
                </Item>
              ))}
            </ItemGroup>
          </Card>
        )}
      </section>
      {editing && (
        <TargetEditor
          key={editing === "new" ? "new" : editing.id}
          projectId={scope.project}
          profile={scope.cloud}
          target={editing === "new" ? undefined : editing}
          siblingTags={cloudTags(
            targets,
            editing === "new" ? undefined : editing
          )}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            onSaved()
          }}
        />
      )}
    </div>
  )
}
