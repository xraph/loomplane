// New project: a repository, a name, and an icon and color to know it by.
// Leaving either unpicked is fine; the project gets a random one.
import { DicesIcon, ShuffleIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

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
import {
  ProjectColorPicker,
  ProjectIconPicker,
} from "@/components/project-style-picker"
import {
  ProjectIcon,
  type ProjectColorName,
  projectColors,
  type ProjectIconName,
  projectIcons,
  randomColor,
  randomIcon,
} from "@/lib/project-style"
import { navigate } from "@/mock/router"
import { useStore } from "@/mock/store"

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
        {open && <NewProjectForm done={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

const repoPattern = /^[\w.-]+\/[\w.-]+$/

function slugify(s: string) {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
}

function NewProjectForm({ done }: { done: () => void }) {
  const { createProject, projects } = useStore()
  const [repo, setRepo] = useState("")
  const [typedName, setTypedName] = useState<string | null>(null)
  const [description, setDescription] = useState("")
  const [icon, setIcon] = useState<ProjectIconName | null>(null)
  const [color, setColor] = useState<ProjectColorName | null>(null)
  const [submitted, setSubmitted] = useState(false)

  // The name follows the repository until someone types their own.
  const name = slugify(typedName ?? repo.split("/")[1] ?? "")
  const repoOk = repoPattern.test(repo)
  const taken = projects.some((p) => p.id === name)
  const nameOk = name.length > 0 && !taken
  const valid = repoOk && nameOk

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!valid) return
    const created = createProject({ repo, name, description, icon, color })
    done()
    navigate(`/projects/${created.id}`)
    const random = !icon || !color
    toast.success(`Created ${created.name}`, {
      description: random
        ? `It got the ${projectIcons[created.icon].label.toLowerCase()} icon in ${projectColors[created.color].label.toLowerCase()} at random. Click the icon on the project page to change it.`
        : "Deploy it and Loomplane maps the services it finds.",
    })
  }

  return (
    <form onSubmit={submit} className="grid gap-5">
      <DialogHeader>
        <DialogTitle>New project</DialogTitle>
        <DialogDescription>
          Point Loomplane at a repository. It finds the services and resources
          on the first deploy.
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
          <ItemTitle>{name || "your-project"}</ItemTitle>
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
        <Field data-invalid={submitted && !repoOk}>
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
              aria-invalid={submitted && !repoOk}
              onChange={(e) => setRepo(e.target.value.trim())}
              className="font-mono text-sm"
              autoComplete="off"
              spellCheck={false}
            />
          </InputGroup>
          {submitted && !repoOk && (
            <FieldError>Use owner/name, like acme/storefront.</FieldError>
          )}
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field data-invalid={submitted && !nameOk}>
            <FieldLabel htmlFor="np-name">Name</FieldLabel>
            <Input
              id="np-name"
              value={typedName ?? name}
              placeholder="storefront"
              aria-invalid={submitted && !nameOk}
              onChange={(e) => setTypedName(e.target.value)}
              className="font-mono"
              autoComplete="off"
            />
            {taken ? (
              <FieldError>{name} is already a project here.</FieldError>
            ) : (
              <FieldDescription>Used in URLs and the CLI.</FieldDescription>
            )}
          </Field>
          <Field>
            <FieldLabel htmlFor="np-description">Description</FieldLabel>
            <Input
              id="np-description"
              value={description}
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
      </FieldGroup>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={done}>
          Cancel
        </Button>
        <Button type="submit">Create project</Button>
      </DialogFooter>
    </form>
  )
}
