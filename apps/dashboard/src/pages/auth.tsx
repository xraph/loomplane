import {
  MFAChallengeFormStyled,
  SignInForm,
  SignUpForm,
  WaitlistForm,
} from "@authsome/ui-components"
import { useAuth } from "@authsome/ui-react"
import { useState } from "react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { useInstance } from "@/lib/instance"
import { usePendingInvite } from "@/lib/invite"
import { useNavigate } from "@/lib/router"

/** Sign-in and sign-up, from authsome's own components. App puts them in the shared AuthLayout. */
export function AuthPage({
  mode,
  notice,
}: {
  mode: "sign-in" | "sign-up"
  notice?: string
}) {
  const navigate = useNavigate()
  const { state, signOut } = useAuth()
  const { waitlist } = useInstance()
  const invite = usePendingInvite()
  // Someone approved from the waitlist can skip to the sign-up form.
  const [approved, setApproved] = useState(false)
  // An invitation or a link carrying an address already has a way in.
  const emailParam = new URLSearchParams(window.location.search).get("email")
  const joinWaitlist = waitlist && !invite && !emailParam && !approved
  return (
    <>
      {notice && (
        <Alert>
          <AlertDescription>{notice}</AlertDescription>
        </Alert>
      )}
      {mode === "sign-in" && state.status === "mfa_required" ? (
        // The password was right and the account has two-step on. A code
        // from the authenticator app, or a recovery code, finishes signing
        // in; the app moves on once the session exists.
        <>
          <MFAChallengeFormStyled className="auth-card" />
          <Button
            variant="link"
            className="h-8 px-0 text-foreground"
            onClick={() => void signOut()}
          >
            Use a different account
          </Button>
        </>
      ) : mode === "sign-in" ? (
        <SignInForm
          title="Welcome back"
          description="Sign in to your projects and environments."
          signUpUrl="/sign-up"
          forgotPasswordUrl="/forgot-password"
          verifyEmailUrl="/verify-email"
          align="left"
          variant="borderless"
          className="auth-card"
        />
      ) : joinWaitlist ? (
        <>
          <WaitlistForm
            signInUrl="/sign-in"
            align="left"
            variant="borderless"
            className="auth-card"
          />
          <Button
            variant="link"
            className="h-8 px-0 text-foreground"
            onClick={() => setApproved(true)}
          >
            Approved already? Create your account
          </Button>
        </>
      ) : (
        <SignUpForm
          signInUrl="/sign-in"
          align="left"
          variant="borderless"
          className="auth-card"
          onSuccess={() => {
            // authsome's sign-up doesn't sign in; the account exists now.
            toast.success("Account created. Sign in to continue.")
            void navigate({ to: "/sign-in", replace: true })
          }}
          onVerificationRequired={(email) => {
            // Sign-in refuses the account until it enters the code it was
            // sent, so go straight to entering it.
            toast.success("Account created. Check your email for a code.")
            void navigate({
              to: "/verify-email",
              search: { email },
              replace: true,
            })
          }}
        />
      )}
    </>
  )
}
