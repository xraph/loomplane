import { type ReactNode, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useProjectsDelete,
  useProjectsGet,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
import { SiteHeader } from "@/components/site-header"
import { isReady, StatusBadge } from "@/components/status-badge"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Weave, type WeftRow } from "@/components/weave"
import { formatDate, repoLabel } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"

// Every new project gets these. Until its environments exist they show as
// threads still being set up.
const defaultEnvironments = ["production", "preview"]

export function ProjectDetail({ id }: { id: string }) {
  const { can } = useOrganization()
  const project = useQuery(useProjectsGet, { path: { id } })
  const environments = useQuery(useEnvironmentsList, { path: { id } })
  const settingUp = !!project.data && !isReady(project.data.status)

  // The environments arrive from a background job; watch until it's done.
  useRefetchWhile(settingUp, project.refetch)
  useRefetchWhile(settingUp, environments.refetch)

  const missing = project.status === "error" && statusOf(project.error) === 404

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/" },
          { label: project.data?.name ?? "Project" },
        ]}
      />
      <div className="flex flex-1 flex-col gap-6 p-4 md:p-6">
        {project.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {missing
                ? "This project doesn't exist in this organization."
                : messageOf(project.error, "The project couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!project.data && project.status !== "error" && (
          <div className="flex flex-col gap-4" aria-busy="true">
            <Skeleton className="h-8 w-64" />
            <Skeleton className="h-40 w-full" />
          </div>
        )}
        {project.data && (
          <Loaded
            project={project.data}
            environments={environments.data?.environments ?? []}
            mayDelete={can("project:delete")}
          />
        )}
      </div>
    </>
  )
}

function Loaded({
  project,
  environments,
  mayDelete,
}: {
  project: Project
  environments: Environment[]
  mayDelete: boolean
}) {
  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight">
            {project.name}
          </h1>
          {project.description && (
            <p className="max-w-prose text-sm text-muted-foreground">
              {project.description}
            </p>
          )}
        </div>
        <StatusBadge status={project.status} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Environments</CardTitle>
          <CardDescription>
            {isReady(project.status)
              ? "Where this project deploys. Services and providers are woven in as you add them."
              : "Loomplane is setting up this project's environments."}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Weave
            rows={weftRows(project, environments)}
            label={`Environments of ${project.name}`}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Details</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-4 text-sm sm:grid-cols-2">
            <Fact label="Repository">
              {project.repositoryUrl ? (
                <code className="font-mono">
                  {repoLabel(project.repositoryUrl)}
                </code>
              ) : (
                <span className="text-muted-foreground">
                  Not connected. Repository import arrives with discovery.
                </span>
              )}
            </Fact>
            <Fact label="Slug">
              <code className="font-mono">{project.slug}</code>
            </Fact>
            <Fact label="Created">
              <time dateTime={project.createdAt}>
                {formatDate(project.createdAt)}
              </time>
            </Fact>
            <Fact label="Project ID">
              <code className="font-mono break-all">{project.id}</code>
            </Fact>
          </dl>
        </CardContent>
      </Card>

      {mayDelete && <DeleteProject project={project} />}
    </>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

/**
 * weftRows lists a project's environments as threads: the defaults first,
 * each ready once it exists and the project has finished setting up.
 */
export function weftRows(
  project: Project,
  environments: Environment[]
): WeftRow[] {
  const exist = new Set(environments.map((e) => e.name))
  const names = [
    ...new Set([...defaultEnvironments, ...environments.map((e) => e.name)]),
  ]
  return names.map((name) => ({
    name,
    ready: exist.has(name) && isReady(project.status),
  }))
}

function DeleteProject({ project }: { project: Project }) {
  const navigate = useNavigate()
  const remove = useMutation(useProjectsDelete)
  // Controlled: Base UI's AlertDialogAction is a plain button and doesn't
  // close the dialog, so a failed delete closes it here.
  const [open, setOpen] = useState(false)

  const confirm = async () => {
    try {
      // The delete invalidates project and environment lists for us.
      await remove.mutateAsync({ path: { id: project.id } })
      toast.success(`Deleted ${project.name}`)
      void navigate({ to: "/", replace: true })
    } catch (err) {
      setOpen(false)
      toast.error(messageOf(err, "The project wasn't deleted."))
    }
  }

  return (
    <Card className="border-destructive/40">
      <CardHeader>
        <CardTitle>Delete project</CardTitle>
        <CardDescription>
          Removes the project and its environments from Loomplane. Nothing has
          been deployed from it yet, so no cloud resources are touched.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <AlertDialog open={open} onOpenChange={setOpen}>
          <AlertDialogTrigger
            render={
              <Button variant="destructive" disabled={remove.isPending} />
            }
          >
            Delete project
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete {project.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                Its production and preview environments go with it. This can't
                be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Keep project</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                disabled={remove.isPending}
                onClick={() => void confirm()}
              >
                Delete project
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}
