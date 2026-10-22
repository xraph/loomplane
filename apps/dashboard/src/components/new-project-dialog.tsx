// New project: its repositories, a name, and an icon and color to know it
// by. Leaving the icon or color unpicked is fine; the server picks at
// random.
import { DicesIcon, PlusIcon, ShuffleIcon, XIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useGithubStatus,
  useProjectsCreate,
} from "@/client/forge/loomplane/hooks"
import type { CreateProjectInputBody } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import {
  ProjectColorPicker,
  ProjectIconPicker,
} from "@/components/project-style-picker"
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
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RepositoryPicker } from "@/components/repository-picker"
import { InputGroupAddon, InputGroupButton } from "@/components/ui/input-group"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { aliasFor, aliasProblem } from "@/lib/alias"
import { githubRepo, repositoryURL } from "@/lib/format"
import {
  ProjectIcon,
  type ProjectColorName,
  projectColors,
  type ProjectIconName,
  projectIcons,
  projectStyle,
  randomColor,
  randomIcon,
} from "@/lib/project-style"
import { useNavigate } from "@/lib/router"
import { slugify } from "@/lib/slug"

// maxRepositories is how many a project can start with, as the API allows.
// More can be added once it exists.
const maxRepositories = 10

/** A repository the project starts with, as typed. */
type RepoRow = { key: number; repo: string; alias: string }

/**
 * planAliases is the alias each row gets, and what's wrong with it: one
 * typed that the API won't take, or one another row already has. Rows
 * with no repository get none.
 */
function planAliases(rows: RepoRow[]): { alias: string; problem: string }[] {
  const seen = new Set<string>()
  return rows.map((r) => {
    const name = githubRepo(repositoryURL(r.repo)).split("/")[1] ?? ""
    if (!r.repo.trim()) return { alias: "", problem: "" }
    const typed = r.alias.trim()
    const alias = typed || aliasFor(name || r.repo)
    let problem = typed ? aliasProblem(typed) : ""
    if (!problem && seen.has(alias))
      problem = `Another repository is already called ${alias}. Give this one an alias of its own.`
    seen.add(alias)
    return { alias, problem }
  })
}

export function NewProjectDialog({
  open,
  onOpenChange,
  repository,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The repository the project starts with, as owner/name. */
  repository?: string
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        {/* Mounted only while open, so every opening starts afresh. */}
        {open && (
          <NewProjectForm
            repository={repository}
            done={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function NewProjectForm({
  repository = "",
  done,
}: {
  repository?: string
  done: () => void
}) {
  const navigate = useNavigate()
  const create = useMutation(useProjectsCreate)
  const github = useQuery(useGithubStatus)
  const connected = (github.data?.installations.length ?? 0) > 0
  const [rows, setRows] = useState<RepoRow[]>([
    { key: 0, repo: repository, alias: "" },
  ])
  const [typedName, setTypedName] = useState<string | null>(null)
  const [description, setDescription] = useState("")
  const [icon, setIcon] = useState<ProjectIconName | null>(null)
  const [color, setColor] = useState<ProjectColorName | null>(null)
  const [error, setError] = useState("")

  // The name follows the first repository until someone types their own.
  const repoName = githubRepo(repositoryURL(rows[0].repo)).split("/")[1] ?? ""
  const name = typedName ?? repoName
  const slug = slugify(name)
  const busy = create.isPending
  const several = rows.length > 1
  const aliases = planAliases(rows)
  const clash = aliases.some((a) => a.problem)

  const setRow = (i: number, change: Partial<RepoRow>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...change } : r)))
  const addRow = () => {
    setRows((rs) => [
      ...rs,
      { key: Math.max(...rs.map((r) => r.key)) + 1, repo: "", alias: "" },
    ])
  }
  const removeRow = (i: number) => {
    setRows((rs) => rs.filter((_, j) => j !== i))
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    const repositories = rows
      .filter((r) => r.repo.trim())
      .map((r) => ({
        repository: repositoryURL(r.repo),
        alias: (several && r.alias.trim()) || undefined,
      }))
    const body: CreateProjectInputBody = {
      name: name.trim(),
      repositories: repositories.length > 0 ? repositories : undefined,
      description: description.trim() || undefined,
      icon: icon ?? undefined,
      color: color ?? undefined,
    }
    try {
      // The create invalidates every project list; the cache refetches it.
      const created = await create.mutateAsync({ body })
      done()
      void navigate({ to: "/projects/$id", params: { id: created.id } })
      const style = projectStyle(created)
      toast.success(`Created ${created.name}`, {
        description:
          !icon || !color
            ? `It got the ${projectIcons[style.icon].label.toLowerCase()} icon in ${projectColors[style.color].label.toLowerCase()} at random. Click the icon on the project page to change it.`
            : created.repositories.length > 1
              ? "Analyze it to see the services Loomplane can deploy from its repositories."
              : created.repositories[0]
                ? "Analyze it to see the services Loomplane can deploy."
                : "Add its repository to see what it would deploy.",
      })
    } catch (err) {
      setError(messageOf(err, "The project wasn't created. Try again."))
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-5">
      <DialogHeader>
        <DialogTitle>New project</DialogTitle>
        <DialogDescription>
          Point Loomplane at a repository, or at each one your application is
          built from. It sets up production and preview environments right away,
          and an analysis maps the services in them.
        </DialogDescription>
      </DialogHeader>

      <Item variant="muted">
        <ItemMedia>
          {icon && color ? (
            <ProjectIcon icon={icon} color={color} size="lg" />
          ) : (
            <span className="flex size-10 items-center justify-center rounded-xl border border-dashed text-muted-foreground">
              {icon ? (
                <ProjectIcon icon={icon} color="slate" size="md" />
              ) : (
                <DicesIcon className="size-5" />
              )}
            </span>
          )}
        </ItemMedia>
        <ItemContent>
          <ItemTitle>{name.trim() || "Your project"}</ItemTitle>
          <ItemDescription className="text-xs">
            {icon && color
              ? `${projectIcons[icon].label} icon in ${projectColors[color].label.toLowerCase()}`
              : "Pick an icon and a color below, or leave them and get a random one"}
          </ItemDescription>
        </ItemContent>
        <ItemActions>
          <Button
            type="button"
            variant="outline"
            size="sm"

            onClick={() => {
              setIcon(randomIcon())
              setColor(randomColor())
            }}
          >
            <ShuffleIcon />
            Shuffle
          </Button>
        </ItemActions>
      </Item>

      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="np-repo-0">
            {several ? "Repositories" : "Repository"}
          </FieldLabel>
          {rows.map((r, i) => (
            <div key={r.key} className="grid gap-1.5">
              <div className="flex gap-2">
                <RepositoryPicker
                  id={`np-repo-${i}`}
                  className="flex-1"
                  autoFocus={i === rows.length - 1}
                  placeholder={i === 0 ? "acme/storefront" : "acme/api"}
                  aria-label={several ? `Repository ${i + 1}` : undefined}
                  value={r.repo}
                  onChange={(repo) => setRow(i, { repo })}
                  exclude={rows.filter((_, j) => j !== i).map((o) => o.repo)}
                  end={
                    several && (
                      <InputGroupAddon align="inline-end">
                        <InputGroupButton
                          size="icon-xs"
                          aria-label={`Remove ${r.repo.trim() || `repository ${i + 1}`}`}
                          onClick={() => removeRow(i)}
                        >
                          <XIcon />
                        </InputGroupButton>
                      </InputGroupAddon>
                    )
                  }
                />
                {several && (
                  <Input
                    aria-label={`Alias for ${r.repo.trim() || `repository ${i + 1}`}`}
                    aria-invalid={!!aliases[i].problem}
                    value={r.alias}
                    placeholder={aliases[i].alias || "alias"}
                    onChange={(e) =>
                      setRow(i, { alias: e.target.value.toLowerCase() })
                    }
                    className="w-32 font-mono text-sm"
                    autoComplete="off"
                    spellCheck={false}
                  />
                )}
              </div>
              {aliases[i].problem && (
                <FieldError>{aliases[i].problem}</FieldError>
              )}
            </div>
          ))}
          {rows.length < maxRepositories && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="justify-self-start"
              onClick={addRow}
            >
              <PlusIcon />
              Add another repository
            </Button>
          )}
          <FieldDescription>
            {several
              ? "Each one gets an alias, from its name unless you give it one. Plans and variables name it by that alias, and it can't change later."
              : connected
                ? "From your GitHub connection, or any public repository. You can add it later, or more of them."
                : "Any public GitHub repository. Connect GitHub under Integrations for private ones. You can add it later, or more of them."}
          </FieldDescription>
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field data-invalid={!!name && !slug}>
            <FieldLabel htmlFor="np-name">Name</FieldLabel>
            <Input
              id="np-name"
              value={name}
              placeholder="Storefront"
              maxLength={64}
              onChange={(e) => setTypedName(e.target.value)}
              autoComplete="off"
              required
            />
            {name && !slug ? (
              <FieldError>Use at least one letter or digit.</FieldError>
            ) : (
              <FieldDescription>
                {slug ? (
                  <>
                    Slug <code className="font-mono">{slug}</code>, used in URLs
                  </>
                ) : (
                  "What your team calls it"
                )}
              </FieldDescription>
            )}
          </Field>
          <Field>
            <FieldLabel htmlFor="np-description">Description</FieldLabel>
            <Input
              id="np-description"
              value={description}
              maxLength={500}
              placeholder="What it does, in a few words"
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
        </div>
        <Field>
          <FieldLabel>Icon</FieldLabel>
          <ProjectIconPicker
            value={icon}
            onChange={setIcon}
            className="grid-cols-8 sm:grid-cols-12"
          />
        </Field>
        <Field>
          <FieldLabel>Color</FieldLabel>
          <ProjectColorPicker value={color} onChange={setColor} />
        </Field>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
      </FieldGroup>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={done} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy || !slug || clash}>
          {busy && <Spinner />}
          Create project
        </Button>
      </DialogFooter>
    </form>
  )
}
