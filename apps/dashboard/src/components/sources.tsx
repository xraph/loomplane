// The repositories a project builds from: each one with what was last read
// from it, the controls that analyze one or all of them, and the dialog
// that adds another.
import { FolderGit2Icon, ScanSearchIcon } from "lucide-react"
import { Fragment, type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesCreate,
  useBitbucketWebhookEnable,
  useProjectsGet,
  useProjectsGraph,
  useProjectsList,
  useSourcesCreate,
  useSourcesDelete,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import type {
  Analysis,
  DiscoverySource,
  Project,
  PushState,
  Source,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { useAnalyses } from "@/components/architecture"
import {
  ActionsCell,
  Cell,
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
import { QueryError } from "@/components/query-error"
import { RepositoryPicker } from "@/components/repository-picker"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { isActive, shortSHA, stepLabel } from "@/lib/architecture"
import { relative, repositoryHost, repositoryURL } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useProjectCan } from "@/lib/project-permissions"
import { useNow } from "@/lib/use-runs"

/**
 * refreshRepositories re-reads the project after its sources change: the
 * page, the project list and the sidebar all show the repositories it
 * builds from, and the generated client only refreshes the sources list.
 */
function refreshRepositories(
  invalidate: ReturnType<typeof useInvalidate>,
  projectID: string
) {
  invalidate(useProjectsGet, { path: { id: projectID } })
  invalidate(useProjectsList)
}

/**
 * sourceURL links to a source on its host, at its directory when it has one.
 * Each host has its own page for a directory.
 */
function sourceURL(s: Source): string {
  const base = repositoryURL(s.repository)
  if (!s.root) return base
  const ref = s.ref || "HEAD"
  const host = repositoryHost(s.repository)
  if (host === "github.com") return `${base}/tree/${ref}/${s.root}`
  if (host === "bitbucket.org") return `${base}/src/${ref}/${s.root}`
  return `${base}/-/tree/${ref}/${s.root}`
}

/** hostLabel is what "Open on" names: GitHub, or the key's own host. */
function hostLabel(s: Source): string {
  const host = repositoryHost(s.repository)
  return host === "github.com" ? "GitHub" : host
}

/**
 * Repositories lists what a project builds from, with what was last read
 * from each: Analyze reads one again, its menu removes it.
 */
export function Repositories({ project }: { project: Project }) {
  const can = useProjectCan(project.id)
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  const analyses = useAnalyses(project.id)
  const start = useMutation(useAnalysesCreate)
  const invalidate = useInvalidate()
  const now = useNow(30_000)
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<Source | null>(null)
  // The graph view knows each source's newest analysis however old it is;
  // the analyses list is only the project's most recent few, so a quiet
  // repository would drop off it and look unanalyzed. The project page
  // refetches the view while an analysis runs.
  const view = useQuery(useProjectsGraph, { path: { id: project.id } })
  const states = new Map(
    (view.data?.sources ?? []).map((st) => [st.source.alias, st])
  )
  const list = sources.data?.sources ?? []
  const mayEdit = can("project:update")
  const mayRun = can("analysis:run")
  // Connecting repositories is a workspace permission, which a project's
  // own permissions never list, and what the server checks to turn push on.
  const mayConnect = useOrganization().can("repository:connect")

  const analyze = (alias?: string) =>
    start
      .mutateAsync({
        path: { id: project.id },
        body: alias ? { sources: [alias] } : {},
      })
      .then(() => invalidate(useProjectsGraph, { path: { id: project.id } }))
      .catch((err: unknown) =>
        toast.error(messageOf(err, "The analysis didn't start."))
      )

  return (
    <section aria-label="Repositories" className="grid gap-2.5">
      <CloudSectionHead
        title="Repositories"
        count={sources.data ? list.length : undefined}
        description="Loomplane reads these to find services. It never runs your code to do it."
      >
        {mayRun && list.length > 1 && (
          <Button
            size="sm"
            variant="outline"
            disabled={analyses.running || start.isPending}
            onClick={() => void analyze()}
          >
            <ScanSearchIcon />
            Analyze all
          </Button>
        )}
        {mayEdit && (
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            Add repository
          </Button>
        )}
      </CloudSectionHead>
      {sources.status === "error" ? (
        <QueryError
          error={sources.error}
          fallback="The repositories couldn't be loaded."
          retry={sources.refetch}
        />
      ) : !sources.data ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : list.length === 0 ? (
        <CloudEmpty
          kind="compact"
          icon={FolderGit2Icon}
          title="No repositories yet"
          action={
            mayEdit && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setAdding(true)}
              >
                Add repository
              </Button>
            )
          }
        >
          Add the repository this project builds from, and Loomplane reads it to
          find services.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Repositories"
          columns="120px minmax(0,1.3fr) minmax(0,1fr) 150px"
          head={["Alias", "Repository", "Last read", ""]}
        >
          {list.map((s) => {
            const state = states.get(s.alias)
            const running = !!state?.latest && isActive(state.latest.status)
            const push = pushOff(s)
            return (
              <Fragment key={s.id}>
                <CloudTableRow className={push && "not-last:border-b-0"}>
                  <Cell className="truncate font-mono text-[12.5px]">
                    {s.alias}
                  </Cell>
                  <NameCell
                    tile={<FolderGit2Icon />}
                    name={
                      <a
                        className="font-mono font-medium underline-offset-2 hover:underline"
                        href={sourceURL(s)}
                        target="_blank"
                        rel="noreferrer"
                      >
                        {s.repository}
                        {s.root ? `/${s.root}` : ""}
                      </a>
                    }
                    meta={readFrom(s, state?.shown)}
                  />
                  <Cell className="text-muted-foreground">
                    {lastReadAt(state?.latest, now)}
                  </Cell>
                  <ActionsCell>
                    {mayRun && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={running || start.isPending}
                        onClick={() => void analyze(s.alias)}
                      >
                        Analyze
                      </Button>
                    )}
                    <RowMenu label={`More actions for ${s.alias}`}>
                      <DropdownMenuItem
                        render={
                          <a
                            href={sourceURL(s)}
                            target="_blank"
                            rel="noreferrer"
                          />
                        }
                      >
                        Open on {hostLabel(s)}
                      </DropdownMenuItem>
                      {mayEdit && (
                        <>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            variant="destructive"
                            onClick={() => setRemoving(s)}
                          >
                            Remove {s.alias}
                          </DropdownMenuItem>
                        </>
                      )}
                    </RowMenu>
                  </ActionsCell>
                </CloudTableRow>
                {push && (
                  <PushNotice
                    source={s}
                    push={push}
                    projectID={project.id}
                    mayTurnOn={mayConnect}
                  />
                )}
              </Fragment>
            )
          })}
        </CloudTable>
      )}
      <AddSourceDialog
        project={project}
        open={adding}
        onOpenChange={setAdding}
      />
      <RemoveSource
        project={project}
        source={removing}
        onDone={() => setRemoving(null)}
      />
    </section>
  )
}

/**
 * pushOff is what a source says when a push to its repository doesn't
 * re-analyze it. Hosts that have no say send nothing.
 */
function pushOff(s: DiscoverySource): PushState | undefined {
  return s.push && !s.push.on ? s.push : undefined
}

/**
 * PushNotice says why pushes don't re-analyze a source, with a button that
 * tries to add the webhook again. Only Bitbucket sources carry the state, and
 * its webhook is the one that button adds.
 */
function PushNotice({
  source,
  push,
  projectID,
  mayTurnOn,
}: {
  source: DiscoverySource
  push: PushState
  projectID: string
  mayTurnOn: boolean
}) {
  const enable = useMutation(useBitbucketWebhookEnable)
  const invalidate = useInvalidate()
  const [failure, setFailure] = useState<string | null>(null)
  const turnOn = async () => {
    setFailure(null)
    try {
      await enable.mutateAsync({ body: { repository: source.repository } })
    } catch (err) {
      setFailure(messageOf(err, "Push re-analysis wasn't turned on."))
    } finally {
      // Either way the list says where it stands now.
      invalidate(useSourcesList, { path: { id: projectID } })
    }
  }
  return (
    <div
      role="row"
      className="flex flex-wrap items-center gap-x-3 gap-y-1.5 bg-subtle px-4 py-2 text-xs text-muted-foreground not-last:border-b"
    >
      <div role="cell" className="min-w-0 flex-[1_1_240px]">
        <span className="font-medium text-weld-ink">
          Pushes don&apos;t re-analyze {source.alias}.
        </span>{" "}
        {push.reason ? sentence(push.reason) : null}
        {failure && (
          <p role="alert" className="mt-1 text-destructive">
            {failure}
          </p>
        )}
      </div>
      {mayTurnOn && (
        <div role="cell">
          <Button
            size="sm"
            variant="outline"
            disabled={enable.isPending}
            onClick={() => void turnOn()}
          >
            {enable.isPending && <Spinner />}
            Turn on
          </Button>
        </div>
      )}
    </div>
  )
}

/** sentence ends a reason the server wrote as a clause. */
function sentence(s: string): string {
  const t = s.charAt(0).toUpperCase() + s.slice(1)
  return /[.!?]$/.test(t) ? t : `${t}.`
}

/** readFrom names the branch and commit a source's graph was read from. */
function readFrom(source: Source, shown?: Analysis): string {
  const branch = shown?.ref || source.ref || "Default branch"
  return shown?.commitSha ? `${branch} @ ${shortSHA(shown.commitSha)}` : branch
}

/** lastReadAt says when a source was last analyzed, or what's happening. */
function lastReadAt(latest: Analysis | undefined, now: number): string {
  if (!latest) return "Not analyzed"
  if (isActive(latest.status)) return stepLabel(latest.status)
  if (latest.status === "failed") return "Failed"
  return relative(Date.parse(latest.finishedAt ?? latest.createdAt), now)
}

function RemoveSource({
  project,
  source,
  onDone,
}: {
  project: Project
  source: Source | null
  onDone: () => void
}) {
  const remove = useMutation(useSourcesDelete)
  const invalidate = useInvalidate()
  const confirm = async () => {
    if (!source) return
    try {
      await remove.mutateAsync({
        path: { id: project.id, alias: source.alias },
      })
      refreshRepositories(invalidate, project.id)
      toast.success(`Removed ${source.alias}`)
      onDone()
    } catch (err) {
      toast.error(messageOf(err, "The repository wasn't removed."))
    }
  }
  return (
    <CloudConfirm
      open={!!source}
      onOpenChange={(open) => !open && onDone()}
      title={`Remove ${source?.alias ?? "repository"}?`}
      confirmLabel="Remove"
      pending={remove.isPending}
      onConfirm={() => void confirm()}
    >
      Loomplane stops analyzing it. The alias {source?.alias} stays taken, so
      plans made with it keep meaning {source?.repository}, and you can&apos;t
      give it to another repository later.
    </CloudConfirm>
  )
}

/** AddSourceDialog attaches another repository, or a directory of one. */
export function AddSourceDialog({
  project,
  open,
  onOpenChange,
}: {
  project: Project
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const add = useMutation(useSourcesCreate)
  const invalidate = useInvalidate()
  const [repository, setRepository] = useState("")
  const [alias, setAlias] = useState("")
  const [root, setRoot] = useState("")
  const [ref, setRef] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const s = await add.mutateAsync({
        path: { id: project.id },
        body: {
          repository: repository.trim(),
          alias: alias.trim() || undefined,
          root: root.trim() || undefined,
          ref: ref.trim() || undefined,
        },
      })
      refreshRepositories(invalidate, project.id)
      toast.success(`Added ${s.alias}`, {
        description: "Analyze it to see what it adds to the graph.",
      })
      setRepository("")
      setAlias("")
      setRoot("")
      setRef("")
      onOpenChange(false)
    } catch (err) {
      toast.error(messageOf(err, "The repository wasn't added."))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={(e) => void submit(e)}>
          <DialogHeader>
            <DialogTitle>Add a repository</DialogTitle>
            <DialogDescription>
              Public repositories work as they are. A private one needs the
              GitHub, GitLab or Bitbucket connection to see it, and Loomplane
              checks that before adding it.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="source-repository">Repository</FieldLabel>
              <RepositoryPicker
                id="source-repository"
                value={repository}
                onChange={setRepository}
                placeholder="acme/storefront"
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="source-alias">Alias</FieldLabel>
              <Input
                id="source-alias"
                value={alias}
                onChange={(e) => setAlias(e.target.value.toLowerCase())}
                placeholder="Picked from the repository's name"
                className="font-mono"
                spellCheck={false}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="source-root">Directory</FieldLabel>
              <Input
                id="source-root"
                value={root}
                onChange={(e) => setRoot(e.target.value)}
                placeholder="The whole repository"
                className="font-mono"
                spellCheck={false}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="source-ref">Branch</FieldLabel>
              <Input
                id="source-ref"
                value={ref}
                onChange={(e) => setRef(e.target.value)}
                placeholder="The default branch"
                className="font-mono"
                spellCheck={false}
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={add.isPending || !repository.trim()}
            >
              {add.isPending && <Spinner />}
              Add
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
