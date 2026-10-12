import { FolderGit2Icon, PlusIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useProjectsCreate,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
import { SiteHeader } from "@/components/site-header"
import { isReady, StatusBadge } from "@/components/status-badge"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Textarea } from "@/components/ui/textarea"
import { formatDate, repoLabel } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"
import { slugify } from "@/lib/slug"

export function Projects() {
  const navigate = useNavigate()
  const { can } = useOrganization()
  const list = useQuery(useProjectsList)
  const projects = list.data?.projects
  const [creating, setCreating] = useState(false)
  const mayCreate = can("project:create")

  // New projects set up in the background; watch until they're ready.
  useRefetchWhile(!!projects?.some((p) => !isReady(p.status)), list.refetch)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Projects" }]} />
      <div className="flex flex-1 flex-col gap-6 p-4 md:p-6">
        <div className="flex items-end justify-between gap-4">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">Projects</h1>
            <p className="text-sm text-muted-foreground">
              Each project is one application, however many services and
              repositories it spans.
            </p>
          </div>
          {mayCreate && (
            <Button onClick={() => setCreating(true)}>
              <PlusIcon />
              New project
            </Button>
          )}
        </div>

        {list.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(list.error, "Projects couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}

        {!projects && list.status !== "error" && <ProjectsSkeleton />}

        {projects?.length === 0 && (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <FolderGit2Icon />
              </EmptyMedia>
              <EmptyTitle>No projects yet</EmptyTitle>
              <EmptyDescription>
                {mayCreate
                  ? "Create a project to get its production and preview environments."
                  : "Ask an organization admin to make you a developer so you can create one."}
              </EmptyDescription>
            </EmptyHeader>
            {mayCreate && (
              <EmptyContent>
                <Button onClick={() => setCreating(true)}>
                  Create a project
                </Button>
              </EmptyContent>
            )}
          </Empty>
        )}

        {projects && projects.length > 0 && (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader className="bg-muted">
                <TableRow>
                  <TableHead>Project</TableHead>
                  <TableHead className="hidden md:table-cell">
                    Repository
                  </TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="hidden text-right lg:table-cell">
                    Created
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {projects.map((p) => (
                  <ProjectRow key={p.id} project={p} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>

      <NewProjectDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(p) => {
          setCreating(false)
          toast.success(`Created ${p.name}`)
          void navigate({ to: "/projects/$id", params: { id: p.id } })
        }}
      />
    </>
  )
}

function ProjectRow({ project: p }: { project: Project }) {
  return (
    <TableRow className="relative">
      <TableCell>
        <Link
          to="/projects/$id"
          params={{ id: p.id }}
          className="font-medium after:absolute after:inset-0 hover:underline"
        >
          {p.name}
        </Link>
        <div className="font-mono text-xs text-muted-foreground">{p.slug}</div>
      </TableCell>
      <TableCell className="hidden text-muted-foreground md:table-cell">
        {p.repositoryUrl ? repoLabel(p.repositoryUrl) : "Not connected"}
      </TableCell>
      <TableCell>
        <StatusBadge status={p.status} />
      </TableCell>
      <TableCell className="hidden text-right text-muted-foreground lg:table-cell">
        <time dateTime={p.createdAt}>{formatDate(p.createdAt)}</time>
      </TableCell>
    </TableRow>
  )
}

function ProjectsSkeleton() {
  return (
    <div
      className="flex flex-col gap-2"
      aria-busy="true"
      aria-label="Loading projects"
    >
      {[0, 1, 2].map((i) => (
        <Skeleton key={i} className="h-14 w-full" />
      ))}
    </div>
  )
}

function NewProjectDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (p: Project) => void
}) {
  const create = useMutation(useProjectsCreate)
  const [name, setName] = useState("")
  const [error, setError] = useState("")
  const slug = slugify(name)
  const busy = create.isPending

  const close = (next: boolean) => {
    if (!next) {
      setName("")
      setError("")
      create.reset()
    }
    onOpenChange(next)
  }

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    const repositoryUrl = String(form.get("repository_url") ?? "").trim()
    const description = String(form.get("description") ?? "").trim()
    setError("")
    try {
      // The create invalidates every project list; the cache refetches it.
      const project = await create.mutateAsync({
        body: {
          name: name.trim(),
          repositoryUrl: repositoryUrl || undefined,
          description: description || undefined,
        },
      })
      setName("")
      onCreated(project)
    } catch (err) {
      setError(messageOf(err, "The project wasn't created. Try again."))
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>
              Loomplane sets up its production and preview environments as soon
              as it's created.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="project-name">Name</FieldLabel>
              <Input
                id="project-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Storefront"
                maxLength={64}
                autoFocus
                required
              />
              <FieldDescription>
                {slug ? (
                  <>
                    Slug <code className="font-mono">{slug}</code>
                  </>
                ) : (
                  "Use at least one letter or digit."
                )}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="repository_url">Repository</FieldLabel>
              <Input
                id="repository_url"
                name="repository_url"
                placeholder="https://github.com/acme/storefront"
                inputMode="url"
              />
              <FieldDescription>
                An https URL or a git@ address. You can add it later.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="description">Description</FieldLabel>
              <Textarea
                id="description"
                name="description"
                maxLength={500}
                placeholder="What this application does"
              />
            </Field>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
          </FieldGroup>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => close(false)}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !slug}>
              {busy && <Spinner />}
              Create project
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
