import { ChevronLeftIcon, CloudIcon } from "lucide-react"
import { CloudsIcon, ProjectsIcon } from "@/components/icons"
import * as api from "@/client/forge/loomplane/hooks"
import type { Profile } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import { OrganizationCloud } from "@/components/cloud-targets/org-cloud"
import {
  CloudEmpty,
  CloudError,
} from "@/components/cloud-targets/infrastructure-ui"
import { Page } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { CloudsWorkspace } from "@/pages/clouds"
import { Domains } from "@/components/cloud-targets/domains"

export function CloudDetail({ id, project }: { id: string; project?: string }) {
  const { session, can } = useOrganization()
  return can("organization_cloud:manage") ? (
    <FromOrganization
      key={`${session.orgId}:${id}`}
      id={id}
      project={project}
    />
  ) : project ? (
    <FromProject
      key={`${session.orgId}:${id}:${project}`}
      id={id}
      project={project}
    />
  ) : (
    <Frame missing={false} loading={false} restricted />
  )
}
function FromOrganization({ id, project }: { id: string; project?: string }) {
  const cloud = useQuery(api.useOrganizationCloudsGet, {
    path: { cloudId: id },
  })
  return (
    <Frame
      cloud={cloud.data}
      project={project}
      loading={!cloud.data && cloud.status !== "error"}
      missing={cloud.status === "error" && statusOf(cloud.error) === 404}
      error={
        cloud.status === "error" && statusOf(cloud.error) !== 404
          ? cloud.error
          : undefined
      }
      organization
    />
  )
}
function FromProject({ id, project }: { id: string; project: string }) {
  const clouds = useQuery(api.useCloudsList, { path: { id: project } })
  const cloud = clouds.data?.clouds.find((c): c is Profile => c?.id === id)
  return (
    <Frame
      cloud={cloud}
      project={project}
      loading={!clouds.data && clouds.status !== "error"}
      missing={!!clouds.data && !cloud}
      error={clouds.status === "error" ? clouds.error : undefined}
    />
  )
}
function Frame({
  cloud,
  project,
  organization,
  loading,
  missing,
  error,
  restricted,
}: {
  cloud?: Profile
  project?: string
  organization?: boolean
  loading: boolean
  missing: boolean
  error?: unknown
  restricted?: boolean
}) {
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
          className="inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          All Clouds
        </Link>
        {restricted ? (
          <CloudEmpty
            kind="notice"
            icon={ProjectsIcon}
            title="Choose a project with Cloud access"
          >
            Open this Cloud through a project that has permission to use it.
          </CloudEmpty>
        ) : error !== undefined ? (
          <CloudError
            message={messageOf(error, "The Cloud couldn't be loaded.")}
          />
        ) : missing ? (
          <CloudEmpty
            kind="notice"
            icon={CloudsIcon}
            title="No Cloud here by that ID"
            action={
              <Button nativeButton={false} render={<Link to="/clouds" />}>
                All Clouds
              </Button>
            }
          >
            It may have been deleted, or your project no longer has access.
          </CloudEmpty>
        ) : loading || !cloud ? (
          <div aria-busy="true" className="grid gap-3">
            <Skeleton className="h-12 w-64" />
            <Skeleton className="h-64" />
          </div>
        ) : (
          <>
            <header className="flex items-center gap-3">
              <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-muted">
                <CloudIcon className="size-5" />
              </span>
              <div className="grid min-w-0 gap-1">
                <h1 className="text-2xl font-semibold tracking-tight break-words">
                  {cloud.name}
                </h1>
                <p className="text-xs text-muted-foreground">
                  Shared across the organization · revision {cloud.revision}
                </p>
              </div>
            </header>
            {organization ? (
              <OrganizationCloud cloud={cloud} initialProject={project} />
            ) : (
              project && (
                <>
                  <CloudsWorkspace projectId={project} cloudId={cloud.id} />
                  <Domains cloud={cloud} projectId={project} />
                </>
              )
            )}
          </>
        )}
      </Page>
    </>
  )
}
