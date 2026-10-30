import { AuthCard } from "@authsome/ui-components"
import { type FormEvent, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { useSetupComplete } from "@/client/forge/loomplane/hooks"
import type { SetupResponse } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { BrandLogo } from "@/components/brand-logo"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { useInstance } from "@/lib/instance"

/**
 * SetupPage creates the first account and its organization on a fresh
 * instance. App shows it while the server reports setup pending.
 */
export function SetupPage() {
  const complete = useMutation(useSetupComplete)
  const { setupTokenRequired } = useInstance()
  // The link the API logs carries the token; read it once.
  const [linkToken] = useState(
    () => new URLSearchParams(window.location.search).get("token") ?? ""
  )
  const [typedToken, setTypedToken] = useState("")
  // A stale link must not be a dead end: after a refusal, ask for the token.
  const [tokenRefused, setTokenRefused] = useState(false)
  const askForToken = setupTokenRequired && (!linkToken || tokenRefused)
  const token = typedToken.trim() || linkToken.trim()
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
    setTokenRefused(false)
    try {
      // The response has no identity for the client to type it by.
      const result = (await complete.mutateAsync({
        body: {
          name: name.trim(),
          email: email.trim(),
          password,
          organizationName: organization.trim(),
          ...(token && { token }),
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
      // Matches the refusal in extensions/identity/setup.go ("a valid setup
      // token is required..."). Any other 403 means setup already ran.
      const refusedToken = /setup token/i.test(messageOf(err, ""))
      if (statusOf(err) === 403 && !refusedToken) {
        setTaken(true)
        return
      }
      if (statusOf(err) === 403 && refusedToken) {
        setTokenRefused(true)
      }
      setError(messageOf(err, "Setup didn't finish. Try again."))
    }
  }

  return (
    <AuthCard
      title="Set up Loomplane"
      description="Create the first account. It owns this instance and its first organization."
      logo={<BrandLogo />}
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
            {askForToken && (
              <Field>
                <FieldLabel htmlFor="setup-token">Setup token</FieldLabel>
                <Input
                  id="setup-token"
                  value={typedToken}
                  onChange={(e) => setTypedToken(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  required
                  aria-describedby="setup-token-hint"
                />
                <FieldDescription id="setup-token-hint">
                  Find it in the API's log, or run loomplane setup-token on the
                  server.
                </FieldDescription>
              </Field>
            )}
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
