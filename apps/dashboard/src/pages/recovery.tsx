import {
  AuthCard,
  ForgotPasswordForm,
  ResetPasswordForm,
} from "@authsome/ui-components"
import { useAuth } from "@authsome/ui-react"
import { useState, type FormEvent } from "react"
import { toast } from "sonner"

import { BrandLogo } from "@/components/brand-logo"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateTitle,
} from "@/components/zero-state"
import { messageOf } from "@/client/errors"
import { Link, useNavigate } from "@/lib/router"

/**
 * Authsome owns the password and verification flows behind these public
 * routes. App renders them inside the shared AuthLayout.
 */
export function RecoveryPage({
  mode,
  token,
  email,
}: {
  mode: "forgot-password" | "reset-password" | "verify-email"
  token?: string
  email?: string
}) {
  const { signOut } = useAuth()
  const navigate = useNavigate()
  return mode === "forgot-password" ? (
    <ForgotPasswordForm
      signInUrl="/sign-in"
      logo={<BrandLogo />}
      align="left"
      variant="borderless"
      className="auth-card"
    />
  ) : mode === "reset-password" ? (
    token ? (
      <ResetPasswordForm
        token={token}
        logo={<BrandLogo />}
        className="auth-card"
        onSuccess={() => {
          // A reset revokes every server session. Clear the SDK's cached state too.
          void signOut().then(() => {
            toast.success("Password changed. Sign in with your new password.")
            void navigate({ to: "/sign-in", replace: true })
          })
        }}
      />
    ) : (
      <ZeroState preview={false}>
        <ZeroStateHeader>
          <ZeroStateTitle>This reset link is incomplete</ZeroStateTitle>
          <ZeroStateDescription>
            Request a new link to choose a password.
          </ZeroStateDescription>
        </ZeroStateHeader>
        <Button render={<Link to="/forgot-password" />}>
          Request a reset link
        </Button>
      </ZeroState>
    )
  ) : (
    <VerifyEmail initialEmail={email ?? ""} />
  )
}

function VerifyEmail({ initialEmail }: { initialEmail: string }) {
  const { client, manager } = useAuth()
  const navigate = useNavigate()
  const [email, setEmail] = useState(initialEmail)
  const [code, setCode] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")

  const verify = async (event: FormEvent) => {
    event.preventDefault()
    setPending(true)
    setError("")
    try {
      // The upstream OTP form omits email. Signup deliberately has no session,
      // so the API needs this address to bind the short code to one account.
      await client.verifyEmail({ email: email.trim(), code })
      await manager.refreshNow()
      toast.success("Email verified. You can continue to Loomplane.")
      void navigate({ to: "/sign-in", replace: true })
    } catch (err) {
      setError(messageOf(err, "The code could not be verified. Try again."))
    } finally {
      setPending(false)
    }
  }

  const resend = async () => {
    setPending(true)
    setError("")
    try {
      await client.resendEmailVerification({ email: email.trim() })
      setNotice("If this address needs verification, a new code has been sent.")
    } catch (err) {
      setError(messageOf(err, "The code could not be requested. Try again."))
    } finally {
      setPending(false)
    }
  }

  return (
    <AuthCard
      title="Verify your email"
      description="Enter the code from your verification email."
      logo={<BrandLogo />}
      align="left"
      variant="borderless"
      className="auth-card"
      footer={
        <Link to="/sign-in" className="text-[13px] font-medium">
          Back to sign in
        </Link>
      }
    >
      <form onSubmit={(event) => void verify(event)} className="grid gap-3">
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="grid gap-1.5">
          <Label htmlFor="verify-email">Email address</Label>
          <Input
            id="verify-email"
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            disabled={pending}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="verify-code">Verification code</Label>
          {/* Sized past the frame's compact inputs: the code is the point. */}
          <Input
            id="verify-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="[0-9]{6}"
            maxLength={6}
            placeholder="000000"
            required
            value={code}
            onChange={(event) => setCode(event.target.value)}
            disabled={pending}
            className="h-12! text-center font-mono text-[22px]! tracking-[0.5em]"
          />
        </div>
        <Button type="submit" disabled={pending}>
          Verify email
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => void resend()}
          disabled={pending || !email.trim()}
        >
          Send a new code
        </Button>
        {notice && (
          <p role="status" className="text-[12.5px] text-overdye-ink">
            {notice}
          </p>
        )}
      </form>
    </AuthCard>
  )
}
