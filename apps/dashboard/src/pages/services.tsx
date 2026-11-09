// What is already running for you: the service projects Loomplane deployed,
// and the resources a connected cloud provider runs that your deployments
// can use. Adding either happens in the catalog, on its own page.
import { ChevronRightIcon } from "lucide-react"

import {
  useConnectionsList,
  useProjectsList,
  useProvidersList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { ManagedResources } from "@/components/managed-resources"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"

export function Services() {
  const { can } = useOrganization()
  const projects = useQuery(useProjectsList)
  const providers = useQuery(useProvidersList)
  const connections = useQuery(useConnectionsList)
  // Catalog deploys make their own project with the database icon.
  const yours =
    projects.data?.projects.filter((p) => p.icon === "database") ?? []

  return (
    <>
      <SiteHeader crumbs={[{ label: "Services" }]} />
      <Page>
        <PageHeading
          title="Services"
          description="What runs for you: services Loomplane deployed, and resources your connected cloud providers run in your account."
          actions={
            <Button
              nativeButton={false}
              render={<Link to="/services/catalog" />}
            >
              Browse catalog
            </Button>
          }
        />

        <section aria-label="Your services" className="grid gap-5">
          <div className="grid gap-2">
            <div className="flex items-baseline justify-between">
              <h2 className="text-[13px] font-medium text-muted-foreground">
                Run by Loomplane
              </h2>
              <span className="text-[12.5px] text-muted-foreground">
                {yours.length ? `${yours.length} deployed` : "None yet"}
              </span>
            </div>
            {projects.status === "error" ? (
              <QueryError
                error={projects.error}
                fallback="Projects couldn't be loaded."
                retry={projects.refetch}
              />
            ) : !projects.data ? (
              <Skeleton className="h-16 rounded-xl" />
            ) : yours.length === 0 ? (
              // A quiet line, so the provider-managed list below stays in view.
              <p className="rounded-xl px-4 py-3 text-[12.5px] text-muted-foreground ring-1 ring-border">
                No service projects yet. Deploy a database or cache from the{" "}
                <Link
                  to="/services/catalog"
                  className="text-foreground underline underline-offset-4"
                >
                  catalog
                </Link>
                , or add a service to an existing project.
              </p>
            ) : (
              <div className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
                {yours.map((p) => {
                  const s = projectStyle(p)
                  return (
                    <Link
                      key={p.id}
                      to="/projects/$id"
                      params={{ id: p.id }}
                      className="group flex items-center gap-3 px-4 py-3 text-foreground transition-colors hover:bg-subtle"
                    >
                      <ProjectIcon icon={s.icon} color={s.color} size="md" />
                      <span className="grid min-w-0 flex-1 gap-0.5">
                        <span className="truncate text-sm font-semibold">
                          {p.name}
                        </span>
                        <span className="truncate text-[12.5px] text-muted-foreground">
                          {p.description || "A service project"}
                        </span>
                      </span>
                      <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
                    </Link>
                  )
                })}
              </div>
            )}
          </div>
          {can("infrastructure:read") && (
            <ManagedResources
              providers={providers.data?.providers ?? []}
              connections={connections.data?.connections ?? []}
            />
          )}
        </section>
      </Page>
    </>
  )
}
