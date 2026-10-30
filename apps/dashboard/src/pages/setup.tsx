import { AuthCard } from "@authsome/ui-components"
import { type FormEvent, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { useSetupComplete } from "@/client/forge/loomplane/hooks"
import type { SetupResponse } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

/**
 * SetupPage creates the first account and its organization on a fresh
 * instance. App shows it while the server reports setup pending.
 */
export function SetupPage() {
  const complete = useMutation(useSetupComplete)
  const [name, setName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [organization, setOrganization] = useState("")
  const [mismatch, setMismatch] = useState(false)
  const [error, setError] = useState("")
  const [done, setDone] = useState(false)
  const [taken, setTaken] = useState(false)
  const busy = complete.isPending || done

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const mismatched = password !== confirm
    setMismatch(mismatched)
    if (mismatched) {
      return
    }
    setError("")
    try {
      // The response has no identity for the client to type it by.
      const result = (await complete.mutateAsync({
        body: {
          name: name.trim(),
          email: email.trim(),
          password,
          organizationName: organization.trim(),
        },
      })) as SetupResponse
      setDone(true)
      // A full load, so the dashboard reads the public config again and
      // stops treating the instance as new.
      window.location.assign(
        result.verificationRequired
          ? `/verify-email?email=${encodeURIComponent(email.trim())}`
          : "/"
      )
    } catch (err) {
      if (statusOf(err) === 403) {
        setTaken(true)
        return
      }
      setError(messageOf(err, "Setup didn't finish. Try again."))
    }
  }

  return (
    <AuthCard
      title="Set up Loomplane"
      description="Create the first account. It owns this instance and its first organization."
      align="left"
      variant="borderless"
      className="auth-card"
    >
      {taken ? (
        <Field data-invalid>
          <FieldError>This instance is already set up.</FieldError>
          {/* A full load: the app still thinks setup is pending until it
              reads the public config again, and would send you back here. */}
          <a
            href="/sign-in"
            className={buttonVariants({ variant: "link", size: "sm" })}
          >
            Sign in
          </a>
        </Field>
      ) : (
        <form onSubmit={submit}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="setup-name">Name</FieldLabel>
              <Input
                id="setup-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoComplete="name"
                autoFocus
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="setup-email">Email</FieldLabel>
              <Input
                id="setup-email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                autoComplete="email"
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="setup-password">Password</FieldLabel>
              <Input
                id="setup-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                required
              />
            </Field>
            <Field data-invalid={mismatch || undefined}>
              <FieldLabel htmlFor="setup-confirm">Confirm password</FieldLabel>
              <Input
                id="setup-confirm"
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="new-password"
                required
                aria-invalid={mismatch || undefined}
                aria-describedby={mismatch ? "setup-confirm-error" : undefined}
              />
              {mismatch && (
                <FieldError id="setup-confirm-error">
                  Passwords don't match.
                </FieldError>
              )}
            </Field>
            <Field>
              <FieldLabel htmlFor="setup-organization">
                Organization name
              </FieldLabel>
              <Input
                id="setup-organization"
                value={organization}
                onChange={(e) => setOrganization(e.target.value)}
                autoComplete="organization"
                required
              />
            </Field>
            <Field>
              {error && <FieldError>{error}</FieldError>}
              <Button type="submit" disabled={busy}>
                {busy && <Spinner />}
                Create account
              </Button>
            </Field>
          </FieldGroup>
        </form>
      )}
    </AuthCard>
  )
}
