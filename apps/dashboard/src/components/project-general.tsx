import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useProjectsUpdate } from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { CloudCallout } from "@/components/cloud-targets/cloud-page"
import { CloudSectionHead } from "@/components/cloud-targets/infrastructure-ui"
import {
  ProjectColorPicker,
  ProjectIconPicker,
} from "@/components/project-style-picker"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  type ProjectColorName,
  type ProjectIconName,
  projectStyle,
} from "@/lib/project-style"

// The API's limits (internal/projects/service.go).
const maxName = 64
const maxDescription = 500

/** ProjectChange is what a save sends: only the fields that changed. */
export type ProjectChange = {
  name?: string
  description?: string
  icon?: string
  color?: string
}

/**
 * projectChange compares a form with the project and returns only what
 * differs, trimmed, so the audit trail names the fields someone changed.
 */
export function projectChange(
  project: Project,
  form: { name: string; description: string; icon: string; color: string }
): ProjectChange {
  const style = projectStyle(project)
  const out: ProjectChange = {}
  const name = form.name.trim()
  const description = form.description.trim()
  if (name !== project.name) out.name = name
  if (description !== (project.description ?? "")) out.description = description
  if (form.icon !== style.icon) out.icon = form.icon
  if (form.color !== style.color) out.color = form.color
  return out
}

/**
 * ProjectGeneral edits a project's name, description, icon and color. The
 * slug stays as it is: other systems key on it.
 */
export function ProjectGeneral({
  project,
  mayEdit,
}: {
  project: Project
  mayEdit: boolean
}) {
  const style = projectStyle(project)
  const update = useMutation(useProjectsUpdate)
  const [name, setName] = useState(project.name)
  const [description, setDescription] = useState(project.description ?? "")
  const [icon, setIcon] = useState<string>(style.icon)
  const [color, setColor] = useState<string>(style.color)
  const [error, setError] = useState("")

  const form = { name, description, icon, color }
  const change = projectChange(project, form)
  const dirty = Object.keys(change).length > 0
  const nameMissing = name.trim() === ""

  const reset = () => {
    setName(project.name)
    setDescription(project.description ?? "")
    setIcon(style.icon)
    setColor(style.color)
    setError("")
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!dirty || nameMissing) return
    setError("")
    try {
      const saved = await update.mutateAsync({
        path: { id: project.id },
        body: change,
      })
      // Show what the server kept, trimmed, so the form isn't left dirty.
      setName(saved.name)
      setDescription(saved.description ?? "")
      toast.success(
        change.name ? `Renamed to ${saved.name}` : "Saved the project's details"
      )
    } catch (err) {
      setError(messageOf(err, "The project wasn't saved."))
    }
  }

  return (
    <section aria-label="General" className="grid gap-2.5">
      <CloudSectionHead
        title="General"
        description="What your team calls the project and how its tile looks. Renaming it keeps its slug, so URLs and anything keyed on it still work."
      />
      <form
        onSubmit={submit}
        className="overflow-hidden rounded-xl bg-card ring-1 ring-border"
      >
        <FieldGroup className="p-4">
          <Field data-invalid={nameMissing}>
            <FieldLabel htmlFor="project-name">Name</FieldLabel>
            <Input
              id="project-name"
              value={name}
              maxLength={maxName}
              disabled={!mayEdit}
              autoComplete="off"
              aria-invalid={nameMissing}
              onChange={(e) => setName(e.target.value)}
            />
            {nameMissing ? (
              <FieldError>A project needs a name.</FieldError>
            ) : (
              <FieldDescription>
                Slug <code className="font-mono">{project.slug}</code> stays the
                same.
              </FieldDescription>
            )}
          </Field>
          <Field>
            <FieldLabel htmlFor="project-description">Description</FieldLabel>
            <Textarea
              id="project-description"
              value={description}
              maxLength={maxDescription}
              disabled={!mayEdit}
              rows={2}
              placeholder="What it does, in a few words"
              onChange={(e) => setDescription(e.target.value)}
            />
            <FieldDescription>
              {description.length} of {maxDescription} characters
            </FieldDescription>
          </Field>
          {/* An icon or color can change but not be cleared. */}
          {mayEdit && (
            <>
              <Field>
                <FieldLabel>Icon</FieldLabel>
                <ProjectIconPicker
                  value={icon as ProjectIconName}
                  onChange={(v) => v && setIcon(v)}
                  className="grid-cols-8 sm:grid-cols-12"
                />
              </Field>
              <Field>
                <FieldLabel>Color</FieldLabel>
                <ProjectColorPicker
                  value={color as ProjectColorName}
                  onChange={(v) => v && setColor(v)}
                />
              </Field>
            </>
          )}
          {error && <CloudCallout tone="error" title={error} />}
        </FieldGroup>
        {mayEdit && (
          <div className="flex flex-wrap justify-end gap-2 border-t bg-muted/40 px-4 py-3">
            <Button
              type="button"
              variant="outline"
              disabled={!dirty || update.isPending}
              onClick={reset}
            >
              Reset
            </Button>
            <Button
              type="submit"
              disabled={!dirty || nameMissing || update.isPending}
            >
              {update.isPending && <Spinner />}
              Save
            </Button>
          </div>
        )}
      </form>
    </section>
  )
}
