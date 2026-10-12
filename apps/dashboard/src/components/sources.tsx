// The repositories a project builds from: each one with what was last read
// from it, the controls that analyze one or all of them, and the dialog
// that adds another.
import {
  GitBranchIcon,
  LockIcon,
  PlusIcon,
  ScanSearchIcon,
  Trash2Icon,
} from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesCreate,
  useGithubRepositories,
  useGithubStatus,
  useProjectsGet,
  useProjectsGraph,
  useProjectsList,
  useSourcesCreate,
  useSourcesDelete,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import type { Project, Source } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { useAnalyses } from "@/components/architecture"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { isActive, lastRead } from "@/lib/architecture"
import { githubRepo } from "@/lib/format"
import { useOrganization } from "@/lib/organization"

// Enough suggestions to find a repository by its first letters, few enough
// that the dialog doesn't grow a scrollbar.
const maxSuggestions = 5

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

/** sourceURL links to a source on GitHub, at its directory when it has one. */
function sourceURL(s: Source): string {
  const base = `https://github.com/${s.repository}`
  return s.root ? `${base}/tree/${s.ref || "HEAD"}/${s.root}` : base
}

/** SourcesCard lists a project's repositories, with what was last read from each. */
export function SourcesCard({ project }: { project: Project }) {
  const { can } = useOrganization()
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  const analyses = useAnalyses(project.id)
  const start = useMutation(useAnalysesCreate)
  const invalidate = useInvalidate()
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
    <Card className="pb-0">
      <CardHeader>
        <CardTitle>Repositories</CardTitle>
        <CardDescription>
          Everything this project builds from. Each one is analyzed on its own.
        </CardDescription>
        <CardAction className="flex items-center gap-2">
          {mayRun && list.length > 1 && (
            <Button
              size="sm"
              variant="outline"
              className="rounded-full"
              disabled={analyses.running || start.isPending}
              onClick={() => void analyze()}
            >
              <ScanSearchIcon />
              Analyze all
            </Button>
          )}
          {mayEdit && (
            <Button
              size="sm"
              className="rounded-full"
              onClick={() => setAdding(true)}
            >
              <PlusIcon />
              Add
            </Button>
          )}
        </CardAction>
      </CardHeader>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="pl-6">Alias</TableHead>
            <TableHead>Repository</TableHead>
            <TableHead>Last read</TableHead>
            <TableHead className="pr-6" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.map((s) => {
            const state = states.get(s.alias)
            const running = !!state?.latest && isActive(state.latest.status)
            return (
              <TableRow key={s.id}>
                <TableCell className="pl-6 font-mono text-xs">
                  {s.alias}
                </TableCell>
                <TableCell>
                  <a
                    className="font-mono text-xs underline-offset-4 hover:underline"
                    href={sourceURL(s)}
                    target="_blank"
                    rel="noreferrer"
                  >
                    {s.repository}
                    {s.root ? `/${s.root}` : ""}
                  </a>
                  {s.ref && (
                    <Badge variant="outline" className="ml-2 font-normal">
                      <GitBranchIcon />
                      {s.ref}
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {lastRead(state)}
                </TableCell>
                <TableCell className="pr-6 text-right">
                  {mayRun && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={running || start.isPending}
                      onClick={() => void analyze(s.alias)}
                    >
                      Analyze
                    </Button>
                  )}
                  {mayEdit && (
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      className="text-muted-foreground"
                      aria-label={`Remove ${s.alias}`}
                      onClick={() => setRemoving(s)}
                    >
                      <Trash2Icon />
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
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
    </Card>
  )
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
    <AlertDialog open={!!source} onOpenChange={(open) => !open && onDone()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {source?.alias}?</AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane stops analyzing it. The alias {source?.alias} stays taken,
            so plans made with it keep meaning {source?.repository}, and you
            can't give it to another repository later.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction onClick={() => void confirm()}>
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
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
  const github = useQuery(useGithubStatus)
  const connected = (github.data?.installations.length ?? 0) > 0
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
              GitHub connection to see it, and Loomplane checks that before
              adding it.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="source-repository">Repository</FieldLabel>
              <Input
                id="source-repository"
                value={repository}
                onChange={(e) =>
                  setRepository(githubRepo(e.target.value) || e.target.value)
                }
                placeholder="acme/storefront"
                className="font-mono"
                spellCheck={false}
                required
              />
              {connected && (
                <Suggestions typed={repository} onPick={setRepository} />
              )}
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

/** Suggestions offers the repositories the GitHub connection can read. */
function Suggestions({
  typed,
  onPick,
}: {
  typed: string
  onPick: (fullName: string) => void
}) {
  const repos = useQuery(useGithubRepositories)
  const query = typed.trim().toLowerCase()
  const matches = (repos.data?.repositories ?? [])
    .filter((r) => r.fullName.toLowerCase() !== query)
    .filter((r) => !query || r.fullName.toLowerCase().includes(query))
    .slice(0, maxSuggestions)
  if (matches.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1.5">
      {matches.map((r) => (
        <Button
          key={r.fullName}
          type="button"
          variant="outline"
          size="xs"
          className="rounded-full font-mono font-normal"
          onClick={() => onPick(r.fullName)}
        >
          {r.private && <LockIcon className="text-muted-foreground" />}
          {r.fullName}
        </Button>
      ))}
    </div>
  )
}
