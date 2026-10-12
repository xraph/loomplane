import { type FormEvent, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useCheckOrgSlug as checkOrgSlug,
  useCreateOrganization,
} from "@/client/forge/authsome/hooks"
import { useMutation } from "@/client/hooks"
import { authsomeApp } from "@/client/runtime"
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
import { slugify } from "@/lib/slug"

/**
 * OrganizationForm creates an organization through authsome's API and hands
 * its ID to onCreated, which switches to it.
 */
export function OrganizationForm({
  onCreated,
}: {
  onCreated: (orgId: string) => Promise<void>
}) {
  const create = useMutation(useCreateOrganization)
  const [name, setName] = useState("")
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  const slug = slugify(name)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!slug) {
      setError(
        "Give the organization a name with at least one letter or digit."
      )
      return
    }
    setBusy(true)
    setError("")
    try {
      // Slugs are unique across every organization, and authsome answers a
      // taken one with a 500, so ask first and say what's wrong.
      const { available } = await checkOrgSlug({
        query: { app_id: authsomeApp(), slug },
      }).refetch()
      if (!available) {
        setError(`The address ${slug} is taken. Try a more specific name.`)
        return
      }
      const org = await create.mutateAsync({
        body: { name: name.trim(), slug },
      })
      await onCreated(org.id)
    } catch (err) {
      setError(messageOf(err, "The organization wasn't created. Try again."))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit}>
      <FieldGroup>
        <Field data-invalid={!!error || undefined}>
          <FieldLabel htmlFor="org-name">Organization name</FieldLabel>
          <Input
            id="org-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Acme Engineering"
            autoFocus
            required
            aria-invalid={!!error || undefined}
          />
          <FieldDescription>
            {slug ? (
              <>
                Its address will be <code className="font-mono">{slug}</code>.
              </>
            ) : (
              "Your company's or team's name works well."
            )}
          </FieldDescription>
          {error && <FieldError>{error}</FieldError>}
        </Field>
        <Field>
          <Button type="submit" disabled={busy}>
            {busy && <Spinner />}
            Create organization
          </Button>
        </Field>
      </FieldGroup>
    </form>
  )
}
