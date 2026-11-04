import { Page, PageHeading } from "@/components/page"
import { PageNotFound } from "@/components/route-feedback"
import { PlatformCloudHosts } from "@/components/platform-cloud-hosts"
import { PlatformCloudRegions } from "@/components/platform-cloud-regions"
import { PlatformCloudSettings } from "@/components/platform-cloud-settings"
import { PlatformCloudTenants } from "@/components/platform-cloud-tenants"
import { SiteHeader } from "@/components/site-header"
import { useInstance } from "@/lib/instance"

/** PlatformCloud is the platform owner's page for the shared hosts workspaces build and deploy on. */
export function PlatformCloud() {
  const { platformAdmin } = useInstance()
  if (!platformAdmin) return <PageNotFound />

  return (
    <>
      <SiteHeader crumbs={[{ label: "Platform Cloud" }]} />
      <Page>
        <PageHeading
          title="Platform Cloud"
          description="Hosts you run that any enabled workspace can build and deploy on. Workspaces see regions only, never these hosts."
        />
        <PlatformCloudHosts />
        <PlatformCloudRegions />
        <PlatformCloudTenants />
        <PlatformCloudSettings />
      </Page>
    </>
  )
}
