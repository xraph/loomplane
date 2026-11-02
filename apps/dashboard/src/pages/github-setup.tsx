import { CheckIcon } from "lucide-react"
import { type ReactNode, useEffect, useRef } from "react"

import { messageOf } from "@/client/errors"
import {
  useGithubConnect,
  useGithubRepositories,
  useGithubStatus,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Page } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { buttonVariants } from "@/components/ui/button"
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

  const connected = connect.status === "success" && !!connect.data
  const checking = complete && (connect.status === "idle" || connect.isPending)
  const steps: { label: string; state: StepState }[] = [
    { label: "GitHub sent you back here", state: complete ? "done" : "todo" },
    {
      label: connected
        ? `The App is installed on ${connect.data?.account}`
        : "The App is installed",
      state: connected ? "done" : checking ? "active" : "todo",
    },
    {
      label: "Loomplane can see its repositories",
      state: connected ? "done" : "todo",
    },
  ]

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: "GitHub" },
        ]}
      />
      <Page className="max-w-[560px] px-6 pt-14 md:px-6">
        <section className="flex flex-col gap-4 rounded-2xl bg-card p-6 shadow-[0_8px_30px_oklch(0.2_0.01_50/0.05)] ring-1 ring-border">
          <div className="flex items-center gap-3">
            <ProductIcon product="github" size="lg" />
            <h1 className="text-xl font-semibold tracking-[-0.02em]">
              {connected ? "GitHub connected" : "Connecting GitHub"}
            </h1>
          </div>
          <p className="text-[13.5px] leading-relaxed text-muted-foreground">
            Loomplane checks with GitHub that you installed the App and can see
            what it was installed on.
          </p>
          <ol className="flex flex-col gap-2.5">
            {steps.map((step) => (
              <Step key={step.label} state={step.state}>
                {step.label}
              </Step>
            ))}
          </ol>
          {!complete && (
            <Alert>
              <AlertDescription>
                GitHub didn&apos;t send an installation back, so nothing was
                connected. If you cancelled, that&apos;s all this means.
              </AlertDescription>
            </Alert>
          )}
          {checking && (
            <p
              className="flex items-center gap-2 text-[13px] text-muted-foreground"
              role="status"
            >
              <Spinner />
              Connecting…
            </p>
          )}
          {connected && (
            <p
              className="rounded-lg bg-overdye-tint px-3 py-2.5 text-[13px] leading-normal text-overdye-ink"
              role="status"
            >
              Connected{" "}
              <span className="font-medium">{connect.data?.account}</span>.
              Projects whose repositories it holds can be analyzed now, private
              ones included.
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
              className={cn(
                buttonVariants({ variant: connected ? "default" : "outline" })
              )}
            >
              Back to integrations
            </Link>
          </div>
        </section>
      </Page>
    </>
  )
}

type StepState = "done" | "active" | "todo"

function Step({ state, children }: { state: StepState; children: ReactNode }) {
  return (
    <li className="flex items-center gap-2.5 text-[13px]">
      <span
        aria-hidden="true"
        className={cn(
          "flex size-5 shrink-0 items-center justify-center rounded-full",
          state === "done" && "bg-overdye-tint text-overdye-ink",
          state === "active" && "bg-brand-tint ring-2 ring-brand ring-inset",
          state === "todo" && "bg-muted"
        )}
      >
        {state === "done" && <CheckIcon className="size-[11px] stroke-3" />}
      </span>
      <span
        className={cn(
          state === "todo" && "text-muted-foreground",
          state === "active" && "font-medium"
        )}
      >
        {children}
      </span>
    </li>
  )
}
