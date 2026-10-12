import { FolderGit2Icon, PlusIcon, SearchIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useQuery, useRefetchWhile } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { isReady } from "@/lib/projects"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Item,
  ItemContent,
  ItemDescription,
  ItemFooter,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { Kbd } from "@/components/ui/kbd"
import { Skeleton } from "@/components/ui/skeleton"
import { formatDate, repositoriesLabel } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"

export function Projects() {
  const { can } = useOrganization()
  const { openNewProject } = useShell()
  const list = useQuery(useProjectsList)
  const projects = list.data?.projects
  const [query, setQuery] = useState("")
  const mayCreate = can("project:create")

  // New projects set up in the background; watch until they're ready.
  useRefetchWhile(!!projects?.some((p) => !isReady(p.status)), list.refetch)

  const q = query.trim().toLowerCase()
  const shown = projects?.filter(
    (p) =>
      !q ||
      [p.name, p.slug, p.description, ...p.repositories].some((v) =>
        v.toLowerCase().includes(q)
      )
  )

  return (
    <>
      <SiteHeader crumbs={[{ label: "Projects" }]} />
      <Page>
        <PageHeading
          title="Projects"
          description="Each project is one application, however many services and repositories it spans."
          actions={
            mayCreate && (
              <Button className="rounded-full" onClick={openNewProject}>
                <PlusIcon />
                New project
              </Button>
            )
          }
        />

        {list.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(list.error, "Projects couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}

        {!projects && list.status !== "error" && (
          <div
            className="grid gap-4 md:grid-cols-2 xl:grid-cols-3"
            aria-busy="true"
            aria-label="Loading projects"
          >
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-36 rounded-xl" />
            ))}
          </div>
        )}

        {projects?.length === 0 && (
          <Empty className="border border-dashed">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <FolderGit2Icon />
              </EmptyMedia>
              <EmptyTitle>No projects yet</EmptyTitle>
              <EmptyDescription>
                {mayCreate
                  ? "Create a project from a repository. It gets production and preview environments right away."
                  : "Ask an organization admin to make you a developer so you can create one."}
              </EmptyDescription>
            </EmptyHeader>
            {mayCreate && (
              <EmptyContent>
                <Button className="rounded-full" onClick={openNewProject}>
                  Create a project
                </Button>
              </EmptyContent>
            )}
          </Empty>
        )}

        {projects && projects.length > 0 && (
          <div className="grid gap-4">
            <InputGroup className="w-full sm:w-72">
              <InputGroupAddon>
                <SearchIcon />
              </InputGroupAddon>
              <InputGroupInput
                data-page-search
                placeholder="Search projects"
                aria-label="Search projects"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <InputGroupAddon align="inline-end">
                <Kbd>/</Kbd>
              </InputGroupAddon>
            </InputGroup>
            {shown && shown.length > 0 ? (
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                {shown.map((p) => (
                  <ProjectCard key={p.id} project={p} />
                ))}
              </div>
            ) : (
              <Empty className="border border-dashed">
                <EmptyHeader>
                  <EmptyTitle>No projects match</EmptyTitle>
                  <EmptyDescription>
                    Search by name, slug, description or repository.
                  </EmptyDescription>
                </EmptyHeader>
                <EmptyContent>
                  <Button
                    variant="outline"
                    className="rounded-full"
                    onClick={() => setQuery("")}
                  >
                    Clear the search
                  </Button>
                </EmptyContent>
              </Empty>
            )}
          </div>
        )}
      </Page>
    </>
  )
}

function ProjectCard({ project: p }: { project: Project }) {
  const style = projectStyle(p)
  const ready = isReady(p.status)
  return (
    <Item
      variant="outline"
      className="items-start gap-4 rounded-xl bg-card p-5 shadow-xs transition-shadow hover:shadow-md"
      render={<Link to="/projects/$id" params={{ id: p.id }} />}
    >
      <ItemMedia>
        <ProjectIcon icon={style.icon} color={style.color} size="lg" />
      </ItemMedia>
      <ItemContent className="min-w-0">
        <ItemTitle className="w-full text-base">
          <span className="truncate">{p.name}</span>
          {!ready && (
            <span className="ml-auto flex shrink-0 items-center gap-1.5 text-xs font-normal text-muted-foreground">
              <span className="h-1.5 w-3 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
              Setting up
            </span>
          )}
        </ItemTitle>
        <ItemDescription className="line-clamp-2 min-h-10">
          {p.description || "No description yet."}
        </ItemDescription>
      </ItemContent>
      <ItemFooter className="basis-full gap-3 border-t pt-3 text-xs text-muted-foreground">
        <span className="flex min-w-0 items-center gap-1.5">
          <FolderGit2Icon className="size-3.5 shrink-0" />
          <span className="truncate font-mono">
            {repositoriesLabel(p.repositories) || "No repository yet"}
          </span>
        </span>
        <time dateTime={p.createdAt} className="shrink-0">
          {formatDate(p.createdAt)}
        </time>
      </ItemFooter>
    </Item>
  )
}
