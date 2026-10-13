import { SignInForm, SignUpForm } from "@authsome/ui-components"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { AuthLayout } from "@/components/auth-layout"
import { BrandLogo } from "@/components/brand-logo"
import { useNavigate, usePath } from "@/lib/router"

/** Sign-in and sign-up, from authsome's own components. */
export function AuthPage({
  mode,
  notice,
}: {
  mode: "sign-in" | "sign-up"
  notice?: string
}) {
  const navigate = useNavigate()
  const path = usePath()
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
          align="center"
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
      ) : (
        <SignUpForm
          signInUrl="/sign-in"
          logo={<BrandLogo />}
          align="center"
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
