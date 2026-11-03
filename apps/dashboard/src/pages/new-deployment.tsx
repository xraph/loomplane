import { CloudEmpty } from "@/components/cloud-targets/infrastructure-ui"
import { DeploymentForm, type DeployPreset } from "@/components/deploy-dialog"
import { Page } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"

/** NewDeploymentPage is the deploy screen at /deployments/new. */
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
      <Page className="gap-6">
        {can("deployment:create") ? (
          <DeploymentForm
            key={JSON.stringify(preset)}
            preset={preset}
            done={() => {
              // Cancel goes back to the project it came from.
              if (preset.project)
                void navigate({
                  to: "/projects/$id",
                  params: { id: preset.project },
                })
              else void navigate({ to: "/deployments" })
            }}
          />
        ) : (
          <CloudEmpty kind="notice" title="Deployment access required">
            Ask your workspace administrator for permission to create
            deployments.
          </CloudEmpty>
        )}
      </Page>
    </>
  )
}
