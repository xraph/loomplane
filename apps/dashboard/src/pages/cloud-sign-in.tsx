import { CloudSignInApps } from "@/components/cloud-sign-in-apps"
import { Page, PageHeading } from "@/components/page"
import { PageNotFound } from "@/components/route-feedback"
import { SiteHeader } from "@/components/site-header"
import { useInstance } from "@/lib/instance"

/** CloudSignIn is the platform owner's page for the OAuth apps people sign in to clouds with. */
export function CloudSignIn() {
  const { platformAdmin } = useInstance()
  if (!platformAdmin) return <PageNotFound />

  return (
    <>
      <SiteHeader crumbs={[{ label: "Cloud sign-in" }]} />
      <Page>
        <PageHeading
          title="Cloud sign-in"
          description="Let people in every organization connect a cloud by signing in, instead of pasting a token. Register an OAuth app with the cloud, then add its client ID and secret here. Every organization on this instance uses the same app."
        />
        <CloudSignInApps variant="page" />
      </Page>
    </>
  )
}
