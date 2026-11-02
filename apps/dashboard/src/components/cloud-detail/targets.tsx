// A Cloud's targets and its default: where each runs, and how many running
// services it placed. Editing reuses the target editor and its review.
import { PlusIcon, TargetIcon } from "lucide-react"
import { useState } from "react"

import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Pool,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudConfirm,
  CloudTable,
  CloudTableRow,
  NameCell,
  RowMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { DefaultPlacementSection } from "@/components/cloud-targets/org-cloud"
import { TargetEditor } from "@/components/cloud-targets/target-editor"
import { cloudTags } from "@/components/cloud-targets/workspace"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { cloudDeletionError } from "@/lib/cloud-targets"
import { Link } from "@/lib/router"

import {
  DEFAULT_KEY,
  servicesPlaced,
  whereLabel,
  type CloudScope,
} from "./data"

const columns = "minmax(0,1fr) minmax(0,1.3fr) minmax(0,0.8fr) 96px 104px"

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
  const [deleting, setDeleting] = useState<Target | null>(null)
  const [error, setError] = useState("")
  const remove = useMutation(api.useCloudTargetsDelete)
  const removeOrg = useMutation(api.useOrganizationCloudTargetsDelete)
  const placed = servicesPlaced(usage)
  const add = manage && (
    <Button size="sm" onClick={() => setEditing("new")}>
      <PlusIcon />
      Add target
    </Button>
  )
  const destroy = async (t: Target) => {
    setError("")
    try {
      if (scope.project)
        await remove.mutateAsync({
          path: { id: scope.project, cloudId: scope.cloud.id, targetId: t.id },
        })
      else
        await removeOrg.mutateAsync({
          path: { cloudId: scope.cloud.id, targetId: t.id },
        })
      setDeleting(null)
      onSaved()
    } catch (err) {
      setError(cloudDeletionError(err, "target"))
    }
  }
  return (
    <div className="grid gap-6">
      <section aria-label="Targets" className="grid gap-2.5">
        <CloudSectionHead
          title="Targets"
          count={targets.length}
          description="Renaming or retagging saves at once. Moving a target to another account, region or pool opens a review, and nothing moves until you sync it."
        >
          {add}
        </CloudSectionHead>
        {targets.length === 0 ? (
          <CloudEmpty kind="list" title="No targets yet" action={add}>
            {scope.project
              ? "This project may use the Cloud's default only."
              : "Add a target such as frontend or backend and pick the account it runs on. Without one, services use the default."}
          </CloudEmpty>
        ) : (
          <CloudTable
            label="Targets"
            columns={columns}
            head={["Target", "Runs on", "Tags", "Services", ""]}
          >
            {targets.map((t) => {
              const account =
                t.runtime === "server"
                  ? undefined
                  : connections.find((c) => c.id === t.connectionId)
              return (
                <CloudTableRow key={t.id}>
                  <NameCell
                    tile={<TargetIcon />}
                    name={t.displayName || t.name}
                    meta={<span className="font-mono">{t.name}</span>}
                  />
                  <Cell>
                    <RunsOn
                      target={t}
                      connections={connections}
                      pools={pools}
                    />
                  </Cell>
                  <Cell className="flex flex-wrap gap-1">
                    {(t.tags ?? []).map((tag) => (
                      <span
                        key={tag}
                        className="rounded-[5px] bg-muted px-1.5 font-mono text-[11px]"
                      >
                        {tag}
                      </span>
                    ))}
                  </Cell>
                  <Cell className="text-[12.5px] text-muted-foreground">
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
                  </Cell>
                  <ActionsCell>
                    {manage && (
                      <>
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Edit ${t.name}`}
                          onClick={() => setEditing(t)}
                        >
                          Edit
                        </Button>
                        <RowMenu label={`More actions for ${t.name}`}>
                          {account && (
                            <DropdownMenuItem
                              render={
                                <Link
                                  to="/clouds/accounts/$id"
                                  params={{ id: account.id }}
                                />
                              }
                            >
                              Open account
                            </DropdownMenuItem>
                          )}
                          {account && <DropdownMenuSeparator />}
                          <DropdownMenuItem
                            variant="destructive"
                            onClick={() => {
                              setError("")
                              setDeleting(t)
                            }}
                          >
                            Delete target
                          </DropdownMenuItem>
                        </RowMenu>
                      </>
                    )}
                  </ActionsCell>
                </CloudTableRow>
              )
            })}
          </CloudTable>
        )}
      </section>
      {!scope.project && (
        <DefaultPlacementSection
          cloud={scope.cloud}
          targets={targets}
          columns={columns}
          services={usage ? (placed.get(DEFAULT_KEY) ?? 0) : undefined}
        />
      )}
      <CloudConfirm
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={`Delete ${deleting?.displayName || deleting?.name || "target"}?`}
        confirmLabel="Delete target"
        typeToConfirm={deleting?.name}
        pending={remove.isPending || removeOrg.isPending}
        error={error && <CloudCallout tone="error" title={error} />}
        onConfirm={() => deleting && void destroy(deleting)}
      >
        Services that name this target have nowhere to run until they pick
        another. A target that environments, deployments or recent changes still
        reference can&apos;t be deleted.
      </CloudConfirm>
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
