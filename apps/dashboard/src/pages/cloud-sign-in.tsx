import { CloudSignInApps } from "@/components/cloud-sign-in-apps"
import { Page, PageHeading } from "@/components/page"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { PageNotFound } from "@/components/route-feedback"
import { SiteHeader } from "@/components/site-header"
import { useInstance } from "@/lib/instance"

/** CloudSignIn is the platform owner's page for the OAuth apps people sign in to clouds with. */
export function CloudSignIn() {
  const { platformAdmin, consolePath } = useInstance()
  if (!platformAdmin) return <PageNotFound />

  return (
    <>
      <SiteHeader crumbs={[{ label: "Cloud sign-in" }]} />
      <Page>
        <PageHeading
          title="Cloud sign-in"
          description="Let people in every organization connect a cloud by signing in, instead of pasting a token. Register an OAuth app with the cloud, then add its client ID and secret here. Every organization on this instance uses the same app."
        />
        {consolePath && <ConsolePointer consolePath={consolePath} />}
        <CloudSignInApps variant="page" />
      </Page>
    </>
  )
}

/**
 * ConsolePointer sends a platform admin to the instance console's
 * Integrations section, which manages these apps and the repository hosts'
 * too, checks them with the provider, and shows how every sign-in is doing.
 */
function ConsolePointer({ consolePath }: { consolePath: string }) {
  const href = `${consolePath.replace(/\/+$/, "")}/@integrations`
  return (
    <Alert>
      <AlertTitle>Also in the instance console</AlertTitle>
      <AlertDescription>
        <p>
          The console's Integrations section manages these apps alongside the
          GitHub App, GitLab and Bitbucket, checks an app with its provider
          before you save it, and shows which sign-ins in every organization
          need attention.{" "}
          <a className="underline underline-offset-4" href={href}>
            Open Integrations in the console
          </a>
        </p>
      </AlertDescription>
    </Alert>
  )
}
