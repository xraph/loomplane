import { type ReactNode, useEffect, useRef } from "react"

import { messageOf } from "@/client/errors"
import { Page } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { buttonVariants } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Link, useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

/** The mutation that hands the code and state to the API. */
export type ConnectMutation = {
  mutateAsync: (args: {
    body: { code: string; state: string }
  }) => Promise<unknown>
  status: string
  error: unknown
}

export interface HostCallbackProps {
  /** The host, as people call it: GitLab, Bitbucket. */
  name: string
  mark: ReactNode
  code: string
  state: string
  /** What the host sent when the person refused or it failed. */
  error: string
  connect: ConnectMutation
  /** Refreshes what the connection changed, before going back. */
  onConnected: () => void
}

/**
 * HostCallback is where a code host sends someone back after they sign in.
 * It hands the code and state to the API, which checks that this person
 * began the sign-in here, then goes back to Integrations.
 */
export function HostCallback({
  name,
  mark,
  code,
  state,
  error,
  connect,
  onConnected,
}: HostCallbackProps) {
  const navigate = useNavigate()
  // Once per visit: StrictMode runs effects twice in development, and the
  // code the host sent works only once.
  const sent = useRef(false)
  const complete = !error && !!code && !!state

  useEffect(() => {
    if (sent.current || !complete) return
    sent.current = true
    connect
      .mutateAsync({ body: { code, state } })
      .then(() => {
        onConnected()
        void navigate({ to: "/integrations", search: { tab: "connected" } })
      })
      .catch(() => undefined) // shown from the mutation's own error state
  }, [complete, connect, onConnected, navigate, code, state])

  const message = error
    ? `${name} didn't connect: ${error}.`
    : !complete
      ? `${name} didn't send a sign-in back, so nothing was connected. If you cancelled, that's all this means.`
      : connect.status === "error"
        ? messageOf(connect.error, `${name} couldn't be connected.`)
        : null

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: name },
        ]}
      />
      <Page className="max-w-[560px] px-6 pt-14 md:px-6">
        <section className="flex flex-col gap-4 rounded-2xl bg-card p-6 shadow-[0_8px_30px_oklch(0.2_0.01_50/0.05)] ring-1 ring-border">
          <div className="flex items-center gap-3">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-[11px] bg-foreground text-background [&_svg]:size-5">
              {mark}
            </span>
            <h1 className="text-xl font-semibold tracking-[-0.02em]">
              {message ? `${name} wasn't connected` : `Connecting ${name}`}
            </h1>
          </div>
          {message ? (
            <>
              <Alert variant="destructive">
                <AlertDescription>{message}</AlertDescription>
              </Alert>
              <div>
                <Link
                  to="/integrations"
                  search={{ tab: "connected" }}
                  className={cn(buttonVariants({ variant: "outline" }))}
                >
                  Back to Integrations
                </Link>
              </div>
            </>
          ) : (
            <p
              className="flex items-center gap-2 text-[13px] text-muted-foreground"
              role="status"
            >
              <Spinner />
              Connecting…
            </p>
          )}
        </section>
      </Page>
    </>
  )
}
