// New project: a repository, a name, and an icon and color to know it by.
// Leaving the icon or color unpicked is fine; the server picks at random.
import { DicesIcon, LockIcon, ShuffleIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useGithubRepositories,
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
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupText,
} from "@/components/ui/input-group"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
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

// Enough suggestions to find a repository by its first letters, few enough
// that the dialog doesn't grow a scrollbar.
const maxSuggestions = 5

export function NewProjectDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        {/* Mounted only while open, so every opening starts empty. */}
        {open && <NewProjectForm done={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function NewProjectForm({ done }: { done: () => void }) {
  const navigate = useNavigate()
  const create = useMutation(useProjectsCreate)
  const github = useQuery(useGithubStatus)
  const connected = (github.data?.installations.length ?? 0) > 0
  const [repo, setRepo] = useState("")
  const [typedName, setTypedName] = useState<string | null>(null)
  const [description, setDescription] = useState("")
  const [icon, setIcon] = useState<ProjectIconName | null>(null)
  const [color, setColor] = useState<ProjectColorName | null>(null)
  const [error, setError] = useState("")

  // The name follows the repository until someone types their own.
  const repoName = githubRepo(repositoryURL(repo)).split("/")[1] ?? ""
  const name = typedName ?? repoName
  const slug = slugify(name)
  const busy = create.isPending

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    const body: CreateProjectInputBody = {
      name: name.trim(),
      repositoryUrl: repo.trim() ? repositoryURL(repo) : undefined,
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
            : created.repositoryUrl
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
          Point Loomplane at a repository. It sets up production and preview
          environments right away, and an analysis maps the services in it.
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
            className="rounded-full"
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
          <FieldLabel htmlFor="np-repo">Repository</FieldLabel>
          <InputGroup>
            <InputGroupAddon>
              <InputGroupText>github.com/</InputGroupText>
            </InputGroupAddon>
            <InputGroupInput
              id="np-repo"
              autoFocus
              placeholder="acme/storefront"
              value={repo}
              onChange={(e) => {
                // A pasted URL becomes owner/name, which is what fits here.
                const v = e.target.value
                setRepo(githubRepo(v) || v.trim())
              }}
              className="font-mono text-sm"
              autoComplete="off"
              spellCheck={false}
            />
          </InputGroup>
          {connected && <Suggestions typed={repo} onPick={setRepo} />}
          <FieldDescription>
            {connected
              ? "From your GitHub connection, or any public repository. You can add it later."
              : "Any public GitHub repository. Connect GitHub under Integrations for private ones. You can add it later."}
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
        <Button type="submit" disabled={busy || !slug}>
          {busy && <Spinner />}
          Create project
        </Button>
      </DialogFooter>
    </form>
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
