import { useAuth } from "@authsome/ui-react"

import { OnboardingLayout } from "@/components/onboarding-layout"
import { OrganizationForm } from "@/components/organization-form"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

const footerLink =
  "text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"

/**
 * Onboarding asks a signed-in person to create a workspace: the first one,
 * when they have none, or another from the sidebar's New workspace. With a
 * workspace already, it also leads back to that one. Signing out sits in
 * the layout's header, beside who's signed in.
 */
export function Onboarding({
  activate,
  current,
}: {
  activate: (orgId: string) => Promise<void>
  /** The workspace you came from, if you have one. */
  current?: string
}) {
  const { user, signOut } = useAuth()
  return (
    <OnboardingLayout
      title={current ? "Create another workspace" : "Create your workspace"}
      lede="A workspace holds your projects, clouds and teammates. Its address is the first part of every dashboard link."
      account={user?.email}
      onSignOut={() => void signOut()}
      footer={
        current && (
          <Link to="/" className={footerLink}>
            Back to {current}
          </Link>
        )
      }
    >
      <OrganizationForm onCreated={activate} />
    </OnboardingLayout>
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
