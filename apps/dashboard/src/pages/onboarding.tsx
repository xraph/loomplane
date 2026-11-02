import { AuthCard } from "@authsome/ui-components"
import { useAuth } from "@authsome/ui-react"

import { AuthLayout } from "@/components/auth-layout"
import { OrganizationForm } from "@/components/organization-form"
import { Button, buttonVariants } from "@/components/ui/button"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

/**
 * Onboarding asks a signed-in person to create a workspace: the first one,
 * when they have none, or another from the sidebar's New workspace. With
 * a workspace already, the way out leads back to it instead of signing out.
 */
export function Onboarding({
  activate,
  current,
}: {
  activate: (orgId: string) => Promise<void>
  /** The workspace you came from, if you have one. */
  current?: string
}) {
  const { signOut } = useAuth()
  return (
    <AuthLayout
      title={current ? "Create another workspace" : "Create your workspace"}
    >
      <AuthCard
        title="Name your workspace"
        description="Give your team's projects a home. You'll be the owner of this workspace."
        align="left"
        variant="borderless"
        className="auth-card"
        footer={
          current ? (
            <Link
              to="/"
              className={cn(
                buttonVariants({ variant: "link" }),
                "h-8 px-0 text-foreground"
              )}
            >
              Back to {current}
            </Link>
          ) : (
            <Button
              variant="link"
              className="h-8 px-0 text-foreground"
              onClick={() => void signOut()}
            >
              Sign out
            </Button>
          )
        }
      >
        <OrganizationForm onCreated={activate} />
      </AuthCard>
    </AuthLayout>
  )
}

/** NewWorkspace is Onboarding for someone who already has a workspace. */
export function NewWorkspace() {
  const { session, organizations, switchOrganization } = useOrganization()
  const current = organizations.find((o) => o.id === session.orgId)
  // Switching runs the gate's onSwitch, which takes you to the new home.
  return (
    <Onboarding
      activate={switchOrganization}
      current={current?.name ?? "your workspace"}
    />
  )
}
