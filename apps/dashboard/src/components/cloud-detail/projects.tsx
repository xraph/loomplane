// The projects allowed on a Cloud: the targets each may use, and the
// environments that actually chose the Cloud.
import { PlusIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Project,
  ProjectGrant,
  Sync,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
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
import { GrantDialog } from "@/components/cloud-targets/org-cloud"
import { ProjectsIcon } from "@/components/icons"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"

import { envState } from "./environments"
import type { CloudScope } from "./data"

export function Projects({
  scope,
  grants,
  projects,
  targets,
  usage,
  syncs,
  manage,
  onSaved,
}: {
  scope: CloudScope
  grants: ProjectGrant[]
  projects: Project[]
  targets: Target[]
  usage?: Usage
  syncs: Sync[]
  manage: boolean
  onSaved: () => void
}) {
  const [editing, setEditing] = useState<ProjectGrant | "new" | null>(null)
  const [removing, setRemoving] = useState<ProjectGrant | null>(null)
  const [error, setError] = useState("")
  const revoke = useMutation(api.useOrganizationCloudsRevokeProject)
  const projectOf = (id: string) => projects.find((p) => p.id === id)
  const available = projects.filter(
    (p) => !grants.some((g) => g.projectId === p.id)
  )
  const give = manage && (
    <Button
      size="sm"
      disabled={available.length === 0}
      onClick={() => setEditing("new")}
    >
      <PlusIcon />
      Give a project access
    </Button>
  )
  const remove = async (g: ProjectGrant) => {
    setError("")
    try {
      await revoke.mutateAsync({
        path: { cloudId: scope.cloud.id, projectId: g.projectId },
      })
      setRemoving(null)
      onSaved()
    } catch (err) {
      setError(messageOf(err, "Access wasn't removed."))
    }
  }
  const removingName = removing
    ? (projectOf(removing.projectId)?.name ?? "this project")
    : ""
  return (
    <section aria-label="Projects with access" className="grid gap-2.5">
      <CloudSectionHead
        title="Projects with access"
        count={grants.length}
        description="Access lets a project's environments choose this Cloud. Removing it stops new placements; what already runs keeps running."
      >
        {give}
      </CloudSectionHead>
      {grants.length === 0 ? (
        <CloudEmpty
          kind="list"
          icon={ProjectsIcon}
          title="No project has access yet"
          action={
            projects.length === 0 ? (
              <Button
                size="sm"
                nativeButton={false}
                variant="outline"
                render={<Link to="/projects" />}
              >
                Create a project
              </Button>
            ) : (
              give
            )
          }
        >
          Give a project the whole Cloud, including its default, or only the
          targets it needs.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Projects with access"
          columns="minmax(0,0.9fr) minmax(0,1.2fr) minmax(0,1.2fr) 128px"
          head={["Project", "May use", "Environments using it", ""]}
        >
          {grants.map((g) => {
            const p = projectOf(g.projectId)
            const style = p ? projectStyle(p) : null
            const allowed = targets.filter((t) => g.targetIds.includes(t.id))
            const envs = (usage?.environments ?? []).filter(
              (e) => e.projectId === g.projectId && e.assigned
            )
            return (
              <CloudTableRow key={g.projectId}>
                <NameCell
                  tile={
                    style && (
                      <ProjectIcon
                        icon={style.icon}
                        color={style.color}
                        className="size-7 rounded-lg"
                      />
                    )
                  }
                  name={
                    p ? (
                      <Link
                        to="/projects/$id"
                        params={{ id: p.id }}
                        className="underline-offset-2 hover:underline"
                      >
                        {p.name}
                      </Link>
                    ) : (
                      <span className="font-normal text-muted-foreground">
                        A project you can&apos;t see
                      </span>
                    )
                  }
                />
                <Cell className="flex flex-wrap gap-1.5 text-xs">
                  {g.targetIds.length === 0 ? (
                    <span className="rounded-md bg-muted px-2 py-0.5">
                      Whole Cloud, with the default
                    </span>
                  ) : (
                    allowed.map((t) => (
                      <span
                        key={t.id}
                        className="rounded-md bg-muted px-2 py-0.5 font-mono"
                      >
                        {t.name}
                      </span>
                    ))
                  )}
                </Cell>
                <Cell className="flex flex-wrap gap-1.5 text-xs">
                  {!usage ? (
                    "…"
                  ) : envs.length === 0 ? (
                    <span className="text-muted-foreground">
                      None chose this Cloud yet
                    </span>
                  ) : (
                    envs.map((e) => {
                      const state = envState(e, syncs)
                      return (
                        <StatusPill key={e.environmentId} tone={state.tone}>
                          {e.environmentName}
                          <span className="sr-only">, {state.label}</span>
                        </StatusPill>
                      )
                    })
                  )}
                </Cell>
                <ActionsCell>
                  {manage && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setEditing(g)}
                    >
                      Edit access
                    </Button>
                  )}
                  {(p || manage) && (
                    <RowMenu
                      label={`More actions for ${p?.name ?? "this project"}`}
                    >
                      {p && (
                        <DropdownMenuItem
                          render={
                            <Link to="/projects/$id" params={{ id: p.id }} />
                          }
                        >
                          Open project
                        </DropdownMenuItem>
                      )}
                      {p && manage && <DropdownMenuSeparator />}
                      {manage && (
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => {
                            setError("")
                            setRemoving(g)
                          }}
                        >
                          Remove access
                        </DropdownMenuItem>
                      )}
                    </RowMenu>
                  )}
                </ActionsCell>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
      <CloudConfirm
        open={!!removing}
        onOpenChange={(open) => {
          if (!open) setRemoving(null)
        }}
        title={`Remove ${removingName}'s access?`}
        confirmLabel="Remove access"
        pending={revoke.isPending}
        error={error && <CloudCallout tone="error" title={error} />}
        onConfirm={() => removing && void remove(removing)}
      >
        Services already running keep running. The project just can&apos;t place
        new ones on {scope.cloud.name}.
      </CloudConfirm>
      {editing && (
        <GrantDialog
          cloud={scope.cloud}
          targets={targets}
          grant={editing === "new" ? undefined : editing}
          projects={editing === "new" ? available : []}
          project={editing === "new" ? undefined : projectOf(editing.projectId)}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            onSaved()
          }}
        />
      )}
    </section>
  )
}
