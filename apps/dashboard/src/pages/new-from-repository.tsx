import { CloudEmpty } from "@/components/cloud-targets/infrastructure-ui"
import { NewProjectDialog } from "@/components/new-project-dialog"
import { Page } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { repositoryKey } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"
import { Projects } from "@/pages/projects"

/**
 * NewFromRepositoryPage is /new?repo=..., where a "Deploy with Loomplane"
 * button in a repository's README lands. It opens New project with that
 * repository filled in, over the projects list. Closing the dialog leaves
 * the list in its place, so a reload doesn't open it again.
 */
export function NewFromRepositoryPage({ repo }: { repo?: string }) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  // "owner/name" for GitHub, "host/path" elsewhere. A URL with a branch or
  // a path after the repository, like GitHub's /tree/main, still reads.
  const repository = repo ? repositoryKey(repo) : ""

  if (!can("project:create"))
    return (
      <>
        <SiteHeader crumbs={[{ label: "New project" }]} />
        <Page>
          <CloudEmpty kind="notice" title="Project access required">
            Ask your workspace administrator for permission to create projects
            {repository ? (
              <>
                , or to create one from{" "}
                <code className="font-mono">{repository}</code> for you
              </>
            ) : null}
            .
          </CloudEmpty>
        </Page>
      </>
    )

  return (
    <>
      <Projects />
      <NewProjectDialog
        open
        repository={repository || undefined}
        onOpenChange={(open) => {
          // Creating the project goes on to its page after this; the
          // history then reads projects, then the new project.
          if (!open) void navigate({ to: "/projects", replace: true })
        }}
      />
    </>
  )
}
