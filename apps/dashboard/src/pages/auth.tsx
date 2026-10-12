import { SignInForm, SignUpForm } from "@authsome/ui-components"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { AuthLayout } from "@/components/auth-layout"
import { BrandLogo } from "@/components/brand-logo"
import { useNavigate } from "@/lib/router"

/** Sign-in and sign-up, from authsome's own components. */
export function AuthPage({
  mode,
  notice,
}: {
  mode: "sign-in" | "sign-up"
  notice?: string
}) {
  const navigate = useNavigate()
  return (
    <AuthLayout
      title={
        mode === "sign-in"
          ? "Sign in to Loomplane"
          : "Create your Loomplane account"
      }
    >
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
          logo={<BrandLogo />}
          align="left"
          variant="borderless"
          className="auth-card"
          onSuccess={() => {
            void navigate({ to: "/", replace: true })
          }}
        />
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
    </AuthLayout>
  )
}
