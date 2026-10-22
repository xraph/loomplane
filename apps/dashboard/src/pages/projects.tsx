import { FolderGit2Icon, PlusIcon, SearchIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useQuery, useRefetchWhile } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { StatusPill, toneOf } from "@/components/runs"
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
import { Kbd } from "@/components/ui/kbd"
import { Skeleton } from "@/components/ui/skeleton"
import { formatDate, repositoriesLabel } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import type { Run } from "@/lib/runs"
import { useRuns } from "@/lib/use-runs"

export function Projects() {
  const { can } = useOrganization()
  const { openNewProject } = useShell()
  const list = useQuery(useProjectsList)
  const projects = list.data?.projects
  const [query, setQuery] = useState("")
  const mayCreate = can("project:create")
  const { runs } = useRuns()

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
              <Button onClick={openNewProject}>
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
          <Empty>
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
                <Button onClick={openNewProject}>Create a project</Button>
              </EmptyContent>
            )}
          </Empty>
        )}

        {projects && projects.length > 0 && (
          <div className="grid gap-4">
            <InputGroup className="h-9 w-full rounded-[10px] border-transparent bg-muted">
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
              <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                {shown.map((p) => (
                  <ProjectCard
                    key={p.id}
                    project={p}
                    runs={runs.filter((r) => r.projectId === p.id)}
                  />
                ))}
                {mayCreate && !q && (
                  <button
                    type="button"
                    onClick={openNewProject}
                    className="flex min-h-40 flex-col items-center justify-center gap-2 rounded-xl border border-dashed text-[13.5px] font-medium text-muted-foreground transition-colors hover:bg-subtle hover:text-foreground"
                  >
                    <span className="flex size-8 items-center justify-center rounded-[9px] bg-brand-tint text-brand-ink">
                      <PlusIcon className="size-4" />
                    </span>
                    New project from a repository
                    <span className="text-xs font-normal">
                      Production and preview are set up for you
                    </span>
                  </button>
                )}
              </div>
            ) : (
              <Empty>
                <EmptyHeader>
                  <EmptyTitle>No projects match</EmptyTitle>
                  <EmptyDescription>
                    Search by name, slug, description or repository.
                  </EmptyDescription>
                </EmptyHeader>
                <EmptyContent>
                  <Button
                    variant="outline"

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

const envLabel: Partial<Record<Run["status"], string>> = {
  succeeded: "Live",
  waiting: "Needs approval",
  running: "Deploying",
  approved: "Deploying",
  failed: "Failed",
  blocked: "Blocked",
}

function ProjectCard({ project: p, runs }: { project: Project; runs: Run[] }) {
  const style = projectStyle(p)
  const ready = isReady(p.status)
  // The newest run of each environment says where it stands.
  const envs = [...new Set(runs.map((r) => r.environment))].map(
    (env) => runs.find((r) => r.environment === env) as Run
  )
  return (
    <Link
      to="/projects/$id"
      params={{ id: p.id }}
      className="group flex flex-col overflow-hidden rounded-xl bg-card text-foreground ring-1 ring-border transition-shadow hover:shadow-[0_6px_18px_oklch(0.2_0.01_50/0.08)]"
    >
      <div className="flex flex-1 flex-col gap-2.5 p-4">
        <div className="flex items-center gap-3">
          <ProjectIcon icon={style.icon} color={style.color} size="lg" />
          <div className="grid min-w-0 flex-1 gap-0.5">
            <span className="truncate text-[14.5px] font-semibold tracking-tight">
              {p.name}
            </span>
            <span className="truncate font-mono text-[11.5px] text-muted-foreground">
              {p.slug}
            </span>
          </div>
          {!ready && (
            <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
              <span className="h-1.5 w-3 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
              Setting up
            </span>
          )}
        </div>
        <p className="line-clamp-2 min-h-10 text-[13px] leading-relaxed text-muted-foreground">
          {p.description || "No description yet."}
        </p>
        <div className="flex flex-wrap gap-1.5">
          {envs.length === 0 ? (
            <StatusPill tone="quiet">Not deployed yet</StatusPill>
          ) : (
            envs.map((r) => (
              <StatusPill key={r.environment} tone={toneOf(r.status)}>
                <span className="capitalize">{r.environment}</span> ·{" "}
                {envLabel[r.status] ?? "Canceled"}
              </StatusPill>
            ))
          )}
        </div>
      </div>
      <div className="flex items-center justify-between gap-3 border-t bg-subtle px-4 py-2.5 text-xs text-muted-foreground">
        <span className="flex min-w-0 items-center gap-1.5">
          <FolderGit2Icon className="size-3.5 shrink-0" />
          <span className="truncate font-mono">
            {repositoriesLabel(p.repositories) || "No repository yet"}
          </span>
        </span>
        <time dateTime={p.createdAt} className="shrink-0">
          {formatDate(p.createdAt)}
        </time>
      </div>
    </Link>
  )
}
