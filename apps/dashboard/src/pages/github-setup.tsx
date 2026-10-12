import { useEffect, useRef } from "react"

import { messageOf } from "@/client/errors"
import {
  useGithubConnect,
  useGithubRepositories,
  useGithubStatus,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Spinner } from "@/components/ui/spinner"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

export interface GitHubSetupProps {
  installationId: string
  code: string
  state: string
}

/**
 * GitHubSetup is where GitHub sends someone back after they install the
 * App. It hands what GitHub sent to the API, which checks that this person
 * started the installation here and can see it, then connects it.
 */
export function GitHubSetup({ installationId, code, state }: GitHubSetupProps) {
  const connect = useMutation(useGithubConnect)
  const invalidate = useInvalidate()
  // Once per visit: StrictMode runs effects twice in development, and the
  // code GitHub sent works only once.
  const sent = useRef(false)
  const id = Number(installationId)
  const complete = Number.isSafeInteger(id) && id > 0 && !!state

  useEffect(() => {
    if (sent.current || !complete) return
    sent.current = true
    connect
      .mutateAsync({ body: { installationId: id, code, state } })
      .then(() => {
        invalidate(useGithubStatus)
        invalidate(useGithubRepositories)
      })
      .catch(() => undefined) // shown from the mutation's own error state
  }, [complete, connect, invalidate, id, code, state])

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: "GitHub" },
        ]}
      />
      <div className="flex flex-1 flex-col gap-6 p-4 md:p-6">
        <Card className="max-w-xl">
          <CardHeader>
            <CardTitle>
              {connect.status === "success"
                ? "GitHub connected"
                : "Connecting GitHub"}
            </CardTitle>
            <CardDescription>
              Loomplane checks with GitHub that you installed the App and can
              see what it was installed on.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {!complete && (
              <Alert>
                <AlertDescription>
                  GitHub didn&apos;t send an installation back, so nothing was
                  connected. If you cancelled, that&apos;s all this means.
                </AlertDescription>
              </Alert>
            )}
            {complete && (connect.status === "idle" || connect.isPending) && (
              <p className="flex items-center gap-2 text-sm" role="status">
                <Spinner />
                Connecting…
              </p>
            )}
            {connect.status === "success" && connect.data && (
              <p className="text-sm" role="status">
                Connected{" "}
                <span className="font-medium">{connect.data.account}</span>.
                Projects whose repositories it holds can be analyzed now,
                private ones included.
              </p>
            )}
            {connect.status === "error" && (
              <Alert variant="destructive">
                <AlertDescription>
                  {messageOf(connect.error, "GitHub couldn't be connected.")}
                </AlertDescription>
              </Alert>
            )}
            <div>
              <Link
                to="/integrations"
                className={cn(buttonVariants({ variant: "outline" }))}
              >
                Back to integrations
              </Link>
            </div>
          </CardContent>
        </Card>
      </div>
    </>
  )
}
