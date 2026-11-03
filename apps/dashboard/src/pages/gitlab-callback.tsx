import { useEffect, useRef } from "react"

import { messageOf } from "@/client/errors"
import {
  useGitlabConnect,
  useGitlabRepositories,
  useGitlabStatus,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Page } from "@/components/page"
import { GitLabMark } from "@/components/gitlab-mark"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { buttonVariants } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Link, useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

export interface GitLabCallbackProps {
  code: string
  state: string
  /** What GitLab sent when the person refused or it failed. */
  error: string
}

/**
 * GitLabCallback is where GitLab sends someone back after they sign in. It
 * hands the code and state to the API, which checks that this person began
 * the sign-in here, then goes back to Integrations.
 */
export function GitLabCallback({ code, state, error }: GitLabCallbackProps) {
  const connect = useMutation(useGitlabConnect)
  const invalidate = useInvalidate()
  const navigate = useNavigate()
  // Once per visit: StrictMode runs effects twice in development, and the
  // code GitLab sent works only once.
  const sent = useRef(false)
  const complete = !error && !!code && !!state

  useEffect(() => {
    if (sent.current || !complete) return
    sent.current = true
    connect
      .mutateAsync({ body: { code, state } })
      .then(() => {
        invalidate(useGitlabStatus)
        invalidate(useGitlabRepositories)
        void navigate({ to: "/integrations", search: { tab: "connected" } })
      })
      .catch(() => undefined) // shown from the mutation's own error state
  }, [complete, connect, invalidate, navigate, code, state])

  const message = error
    ? `GitLab didn't connect: ${error}.`
    : !complete
      ? "GitLab didn't send a sign-in back, so nothing was connected. If you cancelled, that's all this means."
      : connect.status === "error"
        ? messageOf(connect.error, "GitLab couldn't be connected.")
        : null

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: "GitLab" },
        ]}
      />
      <Page className="max-w-[560px] px-6 pt-14 md:px-6">
        <section className="flex flex-col gap-4 rounded-2xl bg-card p-6 shadow-[0_8px_30px_oklch(0.2_0.01_50/0.05)] ring-1 ring-border">
          <div className="flex items-center gap-3">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-[11px] bg-foreground text-background">
              <GitLabMark className="size-5" />
            </span>
            <h1 className="text-xl font-semibold tracking-[-0.02em]">
              {message ? "GitLab wasn't connected" : "Connecting GitLab"}
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
