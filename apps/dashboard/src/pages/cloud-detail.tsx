// One Cloud. An organization's Cloud shows its targets and the projects
// allowed to use them; a project's own Cloud keeps the workspace that
// assigns it to an environment, edits its targets and reviews each sync.
import { ChevronLeftIcon, CloudIcon } from "lucide-react"

import { messageOf, statusOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type { Profile } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Page } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { relative } from "@/lib/format"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { OrganizationCloud } from "@/components/cloud-targets/org-cloud"
import { CloudsWorkspace } from "@/pages/clouds"

export function CloudDetail({ id, project }: { id: string; project?: string }) {
  return project ? (
    <FromProject id={id} project={project} />
  ) : (
    <FromOrganization id={id} />
  )
}

function FromProject({ id, project }: { id: string; project: string }) {
  const clouds = useQuery(api.useCloudsList, { path: { id: project } })
  const cloud = clouds.data?.clouds.find((c): c is Profile => c?.id === id)
  return (
    <Frame
      cloud={cloud}
      loading={!clouds.data && clouds.status !== "error"}
      error={clouds.status === "error" ? clouds.error : undefined}
      missing={!!clouds.data && !cloud}
    />
  )
}

function FromOrganization({ id }: { id: string }) {
  const cloud = useQuery(api.useOrganizationCloudsGet, {
    path: { cloudId: id },
  })
  return (
    <Frame
      cloud={cloud.data}
      loading={!cloud.data && cloud.status !== "error"}
      error={
        cloud.status === "error" && statusOf(cloud.error) !== 404
          ? cloud.error
          : undefined
      }
      missing={cloud.status === "error" && statusOf(cloud.error) === 404}
    />
  )
}

function Frame({
  cloud,
  loading,
  error,
  missing,
}: {
  cloud?: Profile
  loading: boolean
  error?: unknown
  missing: boolean
}) {
  const now = useNow()
  const projects = useQuery(api.useProjectsList)
  const owner = projects.data?.projects.find((p) => p.id === cloud?.projectId)
  const style = owner ? projectStyle(owner) : null
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Clouds", to: "/clouds" },
          { label: cloud?.name ?? "Cloud" },
        ]}
      />
      <Page>
        <Link
          to="/clouds"
          className="-mb-2 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          All Clouds
        </Link>
        {error !== undefined ? (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(error, "The Cloud couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        ) : missing ? (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>No Cloud here by that ID</EmptyTitle>
              <EmptyDescription>
                It may have been deleted, or it belongs to another project.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button nativeButton={false} render={<Link to="/clouds" />}>
                All Clouds
              </Button>
            </EmptyContent>
          </Empty>
        ) : loading || !cloud ? (
          <div className="grid gap-4" aria-busy="true">
            <Skeleton className="h-12 w-80" />
            <Skeleton className="h-64 rounded-xl" />
          </div>
        ) : (
          <>
            <header className="flex items-center gap-3.5">
              <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-muted">
                <CloudIcon className="size-5" />
              </span>
              <div className="grid gap-1">
                <h1 className="text-2xl leading-tight font-semibold tracking-[-0.025em]">
                  {cloud.name}
                </h1>
                <span className="flex flex-wrap items-center gap-2 text-[13px] text-muted-foreground">
                  {!cloud.projectId && (
                    <span className="font-medium text-foreground">
                      Shared across the organization
                    </span>
                  )}
                  {owner && style && (
                    <Link
                      to="/projects/$id"
                      params={{ id: owner.id }}
                      className="flex items-center gap-1.5 font-medium text-foreground hover:underline"
                    >
                      <ProjectIcon
                        icon={style.icon}
                        color={style.color}
                        size="xs"
                      />
                      {owner.name}
                    </Link>
                  )}
                  <span>· revision {cloud.revision}</span>
                  <span>
                    · updated {relative(Date.parse(cloud.updatedAt), now)}
                  </span>
                </span>
              </div>
            </header>
            {cloud.projectId ? (
              <CloudsWorkspace projectId={cloud.projectId} cloudId={cloud.id} />
            ) : (
              <OrganizationCloud cloud={cloud} />
            )}
          </>
        )}
      </Page>
    </>
  )
}
