import { AuthCard } from "@authsome/ui-components"
import { useAuth } from "@authsome/ui-react"

import { AuthLayout } from "@/components/auth-layout"
import { BrandLogo } from "@/components/brand-logo"
import { OrganizationForm } from "@/components/organization-form"
import { Button } from "@/components/ui/button"

/** Onboarding asks a signed-in person with no organization to create one. */
export function Onboarding({
  activate,
}: {
  activate: (orgId: string) => Promise<void>
}) {
  const { signOut } = useAuth()
  return (
    <AuthLayout title="Create your organization">
      <AuthCard
        title="Name your organization"
        description="Give your team's projects a home. You'll be the owner of this organization."
        logo={<BrandLogo />}
        align="center"
        variant="borderless"
        className="auth-card"
        footer={
          <Button
            variant="link"
            className="h-8 px-0 text-foreground"
            onClick={() => void signOut()}
          >
            Sign out
          </Button>
        }
      >
        <OrganizationForm onCreated={activate} />
      </AuthCard>
    </AuthLayout>
  )
}
