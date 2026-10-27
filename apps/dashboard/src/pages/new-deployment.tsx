import { DeploymentForm, type DeployPreset } from "@/components/deploy-dialog"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
} from "@/components/ui/empty"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"

export function NewDeploymentPage({ preset }: { preset: DeployPreset }) {
  const navigate = useNavigate()
  const { can } = useOrganization()
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Deployments", to: "/deployments" },
          { label: "New deployment" },
        ]}
      />
      <Page className="gap-5">
        <PageHeading
          title="New deployment"
          description="Choose your services and infrastructure. Review every change before deploying."
        />
        {can("deployment:create") ? (
          <DeploymentForm
            key={JSON.stringify(preset)}
            preset={preset}
            page
            done={() => {
              void navigate({ to: "/deployments" })
            }}
          />
        ) : (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>Deployment access required</EmptyTitle>
              <EmptyDescription>
                Ask your workspace administrator for permission to create
                deployments.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
      </Page>
    </>
  )
}
