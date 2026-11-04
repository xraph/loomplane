import { useAuth } from "@authsome/ui-react"
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { useOauth2DeviceComplete } from "@/client/forge/authsome/hooks"
import type { DeviceCompleteResponse } from "@/client/forge/authsome/types"
import { useMutation } from "@/client/hooks"
import { OnboardingLayout } from "@/components/onboarding-layout"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

/**
 * formatCode reads a sign-in code the way it's printed, XXXX-XXXX, from
 * however it was typed or pasted: any case, with or without the dash.
 */
export function formatCode(raw: string): string {
  const letters = raw
    .toUpperCase()
    .replace(/[^A-Z]/g, "")
    .slice(0, 8)
  return letters.length > 4
    ? `${letters.slice(0, 4)}-${letters.slice(4)}`
    : letters
}

type Outcome = "authorized" | "denied"

/**
 * Device approves a loomctl sign-in code. `loomctl login` prints the code
 * and opens this page with it filled in; approving signs loomctl in as you.
 * It never approves on load: a link alone must not be enough, because the
 * person who sent it would get your session. You're signed in to get here,
 * so it uses the onboarding frame, with the account and signing out above
 * the card.
 */
export function Device({ code: initial }: { code: string }) {
  const { user, signOut } = useAuth()
  const complete = useMutation(useOauth2DeviceComplete)
  const [code, setCode] = useState(() => formatCode(initial))
  const [error, setError] = useState("")
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const ready = code.length === 9
  const busy = complete.isPending

  const decide = async (action: "approve" | "deny") => {
    setError("")
    try {
      // The body is a form in authsome's spec, so the generator leaves the
      // answer untyped; it's this.
      const res = (await complete.mutateAsync({
        body: { user_code: code, action },
      })) as DeviceCompleteResponse
      setOutcome(res.status === "authorized" ? "authorized" : "denied")
    } catch (err) {
      setError(
        statusOf(err) === 404
          ? "That code isn't valid, or it expired. Run loomctl login again for a new one."
          : messageOf(err, "The code couldn't be confirmed. Try again.")
      )
    }
  }

  const account = user?.email
  const leave = () => void signOut()

  if (outcome) {
    return (
      <OnboardingLayout
        title={
          outcome === "authorized" ? "loomctl is signed in" : "Sign-in denied"
        }
        lede={
          outcome === "authorized"
            ? "You can close this tab and go back to your terminal."
            : "loomctl won't be signed in with that code. You can close this tab."
        }
        account={account}
        onSignOut={leave}
      >
        {/* A full load, so the dashboard starts in your workspace. */}
        <Button
          variant="outline"
          className="self-start"
          onClick={() => window.location.assign("/")}
        >
          Open Loomplane
        </Button>
      </OnboardingLayout>
    )
  }

  return (
    <OnboardingLayout
      title="Sign in to loomctl"
      lede="loomctl will act as you in the workspaces you belong to. Only approve a code you started from your own terminal."
      account={account}
      onSignOut={busy ? undefined : leave}
    >
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (ready) void decide("approve")
        }}
      >
        <Field>
          <FieldLabel htmlFor="device-code">
            The code loomctl showed you
          </FieldLabel>
          <Input
            id="device-code"
            value={code}
            onChange={(e) => setCode(formatCode(e.target.value))}
            placeholder="XXXX-XXXX"
            autoComplete="off"
            autoCapitalize="characters"
            spellCheck={false}
            className="font-mono tracking-widest"
            disabled={busy}
          />
        </Field>
        {error && (
          <Alert variant="destructive" className="my-2">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <Button type="submit" disabled={busy || !ready}>
          {busy && <Spinner />}
          Approve
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={() => void decide("deny")}
          disabled={busy || !ready}
        >
          Deny
        </Button>
      </form>
    </OnboardingLayout>
  )
}
