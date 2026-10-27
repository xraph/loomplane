// The projects allowed on a Cloud: the targets each may use, and the
// environments that actually chose the Cloud.
import { PlusIcon } from "lucide-react"
import { useState } from "react"

import type {
  Project,
  ProjectGrant,
  Sync,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import {
  CloudEmpty,
  CloudSectionHead,
  Row,
  Rows,
} from "@/components/cloud-targets/infrastructure-ui"
import { GrantDialog } from "@/components/cloud-targets/org-cloud"
import { ProjectsIcon } from "@/components/icons"
import { toneClass } from "@/components/runs"
import { Button } from "@/components/ui/button"
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
  const projectOf = (id: string) => projects.find((p) => p.id === id)
  const available = projects.filter(
    (p) => !grants.some((g) => g.projectId === p.id)
  )
  return (
    <section aria-label="Projects with access" className="grid gap-3">
      <CloudSectionHead
        title="Projects with access"
        count={grants.length}
        description="Access lets a project's environments choose this Cloud. Removing it stops new placements; what already runs keeps running."
      >
        {manage && (
          <Button
            variant="outline"
            disabled={available.length === 0}
            onClick={() => setEditing("new")}
          >
            <PlusIcon />
            Give a project access
          </Button>
        )}
      </CloudSectionHead>
      {grants.length === 0 ? (
        <CloudEmpty
          icon={ProjectsIcon}
          title="No project has access yet"
          action={
            projects.length === 0 && (
              <Button
                nativeButton={false}
                variant="outline"
                render={<Link to="/projects" />}
              >
                Create a project
              </Button>
            )
          }
        >
          Give a project the whole Cloud, including its default, or only the
          targets it needs.
        </CloudEmpty>
      ) : (
        <Rows>
          {grants.map((g) => {
            const p = projectOf(g.projectId)
            const style = p ? projectStyle(p) : null
            const allowed = targets.filter((t) => g.targetIds.includes(t.id))
            const envs = (usage?.environments ?? []).filter(
              (e) => e.projectId === g.projectId && e.assigned
            )
            return (
              <Row
                key={g.projectId}
                className="flex-wrap items-start gap-y-2 py-3"
              >
                <span className="flex min-w-0 flex-[1_1_200px] items-center gap-2.5">
                  {style && (
                    <ProjectIcon
                      icon={style.icon}
                      color={style.color}
                      size="sm"
                    />
                  )}
                  {p ? (
                    <Link
                      to="/projects/$id"
                      params={{ id: p.id }}
                      className="truncate text-[13px] font-medium hover:underline"
                    >
                      {p.name}
                    </Link>
                  ) : (
                    <span className="truncate text-[13px] text-muted-foreground">
                      A project you can&apos;t see
                    </span>
                  )}
                </span>
                <dl className="grid min-w-0 flex-[2_1_300px] grid-cols-[92px_minmax(0,1fr)] gap-x-2 gap-y-1.5 text-xs">
                  <dt className="py-0.5 text-muted-foreground">May use</dt>
                  <dd className="flex flex-wrap gap-1.5">
                    {g.targetIds.length === 0 ? (
                      <span className="rounded-md bg-muted px-2 py-0.5">
                        Whole Cloud, with the default
                      </span>
                    ) : (
                      allowed.map((t) => (
                        <span
                          key={t.id}
                          className="rounded-md bg-muted px-2 py-0.5"
                        >
                          {t.name}
                        </span>
                      ))
                    )}
                  </dd>
                  <dt className="py-0.5 text-muted-foreground">Environments</dt>
                  <dd className="flex flex-wrap gap-1.5">
                    {!usage ? (
                      "…"
                    ) : envs.length === 0 ? (
                      <span className="py-0.5 text-muted-foreground">
                        None chose this Cloud yet
                      </span>
                    ) : (
                      envs.map((e) => (
                        <span
                          key={e.environmentId}
                          className="inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 ring-1 ring-border"
                        >
                          <span
                            aria-hidden="true"
                            className={`size-1.5 rounded-full ${toneClass[envState(e, syncs).tone].dot}`}
                          />
                          {e.environmentName}
                          <span className="sr-only">
                            , {envState(e, syncs).label}
                          </span>
                        </span>
                      ))
                    )}
                  </dd>
                </dl>
                {manage && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setEditing(g)}
                  >
                    Edit access
                  </Button>
                )}
              </Row>
            )
          })}
        </Rows>
      )}
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
