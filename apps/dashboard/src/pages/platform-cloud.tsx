import { Page, PageHeading } from "@/components/page"
import { PageNotFound } from "@/components/route-feedback"
import { SiteHeader } from "@/components/site-header"
import { useInstance } from "@/lib/instance"

const sections = ["Hosts", "Regions", "Workspaces", "Settings"]

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
        {sections.map((name) => (
          <section key={name} className="flex flex-col gap-2">
            <h2 className="text-base font-medium">{name}</h2>
          </section>
        ))}
      </Page>
    </>
  )
}
