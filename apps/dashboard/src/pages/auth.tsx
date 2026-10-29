import { SignInForm, SignUpForm, WaitlistForm } from "@authsome/ui-components"
import { useState } from "react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { BrandLogo } from "@/components/brand-logo"
import { Button } from "@/components/ui/button"
import { useInstance } from "@/lib/instance"
import { usePendingInvite } from "@/lib/invite"
import { useNavigate, usePath } from "@/lib/router"

/** Sign-in and sign-up, from authsome's own components. App puts them in the shared AuthLayout. */
export function AuthPage({
  mode,
  notice,
}: {
  mode: "sign-in" | "sign-up"
  notice?: string
}) {
  const navigate = useNavigate()
  const path = usePath()
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
      {mode === "sign-in" ? (
        <SignInForm
          title="Welcome back"
          description="Sign in to your projects and environments."
          signUpUrl="/sign-up"
          forgotPasswordUrl="/forgot-password"
          verifyEmailUrl="/verify-email"
          logo={<BrandLogo />}
          align="left"
          variant="borderless"
          className="auth-card"
          onSuccess={() => {
            // Anywhere else, the page you asked for is still in the
            // address bar, and it shows once you're signed in. That
            // matters for GitHub's install redirect, whose one-time code
            // only that page can use.
            if (path === "/sign-in" || path === "/sign-up") {
              void navigate({ to: "/", replace: true })
            }
          }}
        />
      ) : joinWaitlist ? (
        <>
          <WaitlistForm
            signInUrl="/sign-in"
            logo={<BrandLogo />}
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
          logo={<BrandLogo />}
          align="left"
          variant="borderless"
          className="auth-card"
          onSuccess={() => {
            // authsome's sign-up doesn't sign in; the account exists now.
            toast.success("Account created. Sign in to continue.")
            void navigate({ to: "/sign-in", replace: true })
          }}
        />
      )}
    </>
  )
}
