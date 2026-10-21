import { useState } from "react"
import { CloudIcon, PlusIcon } from "lucide-react"
import * as api from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import {
  CloudWorkspace,
  EmptyClouds,
  selectClass,
} from "@/components/cloud-targets/workspace"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

export function Clouds() {
  const { session, can } = useOrganization()
  return (
    <CloudsScope
      key={session.orgId}
      organizationId={session.orgId}
      allowed={can("cloud_account:read")}
    />
  )
}
function CloudsScope({
  allowed,
  organizationId,
}: {
  allowed: boolean
  organizationId: string
}) {
  const projects = useQuery(api.useProjectsList)
  const preference = `loomplane.clouds.project.${organizationId}`
  const [selected, setSelected] = useState(
    () => window.localStorage.getItem(preference) ?? ""
  )
  const project =
    projects.data?.projects.find((p) => p.id === selected) ??
    projects.data?.projects[0]
  return (
    <>
      <SiteHeader crumbs={[{ label: "Clouds" }]} />
      <Page className="gap-5">
        <PageHeading
          title={
            <span className="flex items-center gap-2">
              <CloudIcon className="size-6" />
              Clouds
            </span>
          }
          description="Named targets keep your services connected to the accounts you choose."
        />
        {!allowed ? (
          <EmptyClouds
            title="Cloud access is restricted"
            description="Ask your organization owner for permission to view Cloud accounts."
          />
        ) : projects.status === "error" ? (
          <QueryError
            error={projects.error}
            fallback="Projects couldn't be loaded."
            retry={projects.refetch}
          />
        ) : !projects.data ? (
          <Skeleton className="h-24" />
        ) : !project ? (
          <EmptyClouds
            title="Create a project first"
            description="Clouds and their targets belong to a project."
          >
            <Link to="/projects">
              <Button>
                <PlusIcon />
                Projects
              </Button>
            </Link>
          </EmptyClouds>
        ) : (
          <CloudWorkspace
            key={project.id}
            projectId={project.id}
            projectControl={
              <label className="grid min-w-32 flex-1 gap-1 text-xs text-muted-foreground">
                Project
                <select
                  className={selectClass}
                  aria-label="Project"
                  value={project.id}
                  onChange={(e) => {
                    setSelected(e.target.value)
                    window.localStorage.setItem(preference, e.target.value)
                  }}
                >
                  {projects.data.projects.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </label>
            }
          />
        )}
      </Page>
    </>
  )
}
