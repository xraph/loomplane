import { useEffect, useRef, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useConnectionSignInsComplete,
  useConnectionsGet,
  useConnectionsList,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Page } from "@/components/page"
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
import { Link, useNavigate } from "@/lib/router"
import { signInCloud, takeVercelState } from "@/lib/sign-in-cloud"
import { cn } from "@/lib/utils"

export interface OAuthCallbackProps {
  state: string
  code: string
  error: string
  errorDescription: string
  /** Vercel's install ID, which only its redirect carries. */
  configurationId?: string
}

/**
 * OAuthCallback is where a cloud sends someone back after they sign in. It
 * hands the state and code to the API, which checks that this person started
 * the sign-in here, then opens the connection it made or renewed.
 */
export function OAuthCallback({
  state,
  code,
  error,
  errorDescription,
  configurationId = "",
}: OAuthCallbackProps) {
  const complete = useMutation(useConnectionSignInsComplete)
  const invalidate = useInvalidate()
  const navigate = useNavigate()
  // Once per visit: StrictMode runs effects twice in development, and the
  // code the cloud sent works only once.
  const sent = useRef(false)
  // Vercel can drop the state from its redirect. Then, and only for a Vercel
  // install (it carries a configurationId), the state this browser started
  // with stands in. Reading it removes it, so it's read once, on first render.
  const [kept] = useState(() => {
    const stored = takeVercelState()
    return !state && configurationId ? stored : ""
  })
  const use = state || kept
  const ready = !error && !!use && !!code

  useEffect(() => {
    if (sent.current || !ready) return
    sent.current = true
    complete
      .mutateAsync({ body: { state: use, code } })
      .then((c) => {
        invalidate(useConnectionsList)
        invalidate(useConnectionsGet)
        return navigate({ to: "/clouds/accounts/$id", params: { id: c.id } })
      })
      .catch(() => undefined) // shown from the mutation's own error state
  }, [ready, complete, invalidate, navigate, use, code])

  // The cloud's own words come from the link, which anyone can craft, so
  // they're shown as the cloud's and kept short.
  const cloud = signInCloud() || "The cloud"
  const said = errorDescription.trim().slice(0, 300)
  const failure = error
    ? `${cloud} didn't sign you in, so nothing was connected.`
    : !ready
      ? "The cloud didn't send a sign-in back, so nothing was connected."
      : complete.status === "error"
        ? messageOf(complete.error, "The sign-in couldn't be finished.")
        : ""

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: "Sign in" },
        ]}
      />
      <Page>
        <Card className="max-w-xl">
          <CardHeader>
            <CardTitle>
              {failure ? "Sign-in didn't finish" : "Finishing sign-in"}
            </CardTitle>
            <CardDescription>
              Loomplane checks that you started this sign-in here.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {failure ? (
              <>
                <Alert variant="destructive">
                  <AlertDescription className="grid gap-1">
                    <p>{failure}</p>
                    {error && said && (
                      <p className="text-muted-foreground">
                        {`${cloud} said: ${said}`}
                      </p>
                    )}
                  </AlertDescription>
                </Alert>
                <div>
                  <Link
                    to="/integrations"
                    className={cn(buttonVariants({ variant: "outline" }))}
                  >
                    Back to integrations
                  </Link>
                </div>
              </>
            ) : (
              <p className="flex items-center gap-2 text-sm" role="status">
                <Spinner />
                Finishing…
              </p>
            )}
          </CardContent>
        </Card>
      </Page>
    </>
  )
}
