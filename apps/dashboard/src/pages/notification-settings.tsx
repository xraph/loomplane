// How notifications reach you: your own delivery preferences, plus a way to
// the workspace's webhooks for administrators. The inbox has its own page.
import { ChevronRightIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useNotificationsPreferencesGet,
  useNotificationsPreferencesUpdate,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { WebhooksIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  ReadError,
  useAuthorizationFailure,
  useNotificationRefresh,
} from "@/lib/notifications"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

export function NotificationSettings() {
  const { session, can } = useOrganization()
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Notifications", to: "/notifications" },
          { label: "Settings" },
        ]}
      />
      <Page className="max-w-[980px]">
        <PageHeading
          title="Notification settings"
          description="Choose how deployment results reach you."
        />
        <Preferences key={session.orgId} />
        {can("member:manage") && (
          <Card className="px-4 py-3">
            <Link
              to="/webhooks"
              className="flex items-center gap-3 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="grid size-8 place-items-center rounded-md bg-muted text-muted-foreground [&_svg]:size-4">
                <WebhooksIcon />
              </span>
              <span className="grid flex-1 gap-0.5">
                <span className="text-[14px] font-semibold">
                  Workspace webhooks
                </span>
                <span className="text-[12.5px] text-muted-foreground">
                  Send signed deployment results to your own endpoints.
                </span>
              </span>
              <ChevronRightIcon className="size-4 text-muted-foreground" />
            </Link>
          </Card>
        )}
      </Page>
    </>
  )
}

function Preferences() {
  const preferences = useQuery(useNotificationsPreferencesGet)
  const save = useMutation(useNotificationsPreferencesUpdate)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")
  const denied = useAuthorizationFailure(preferences)
  useNotificationRefresh(preferences.refetch)

  const saveEmail = async (deploymentEmail: boolean) => {
    setError("")
    try {
      await save.mutateAsync({ body: { deploymentEmail } })
      invalidate(useNotificationsPreferencesGet)
    } catch (err) {
      setError(messageOf(err, "Your preference wasn't saved."))
    }
  }

  return (
    <>
      <Card className="gap-3 px-4 py-3">
        {preferences.data && !denied ? (
          <>
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-1">
                <label
                  htmlFor="deployment-email"
                  className="text-[14px] font-semibold"
                >
                  Deployment email
                </label>
                <p className="text-[12.5px] text-muted-foreground">
                  Email results for deployments you started. Account recovery is
                  separate.
                </p>
              </div>
              <Switch
                id="deployment-email"
                checked={preferences.data.deploymentEmail}
                disabled={save.isPending}
                onCheckedChange={(checked) => void saveEmail(checked)}
              />
            </div>
            {!preferences.data.mailEnabled && (
              <p role="status" className="text-xs text-muted-foreground">
                Email delivery is disabled. Your preference is saved for when an
                administrator enables a mail provider.
              </p>
            )}
          </>
        ) : preferences.status === "error" ? (
          <ReadError
            error={preferences.error}
            fallback="Your preferences couldn't be loaded."
            retry={() => void preferences.refetch().catch(() => undefined)}
            label="Retry preferences"
          />
        ) : (
          <Skeleton className="h-12 w-full" />
        )}
      </Card>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </>
  )
}
