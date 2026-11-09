// The cloud edition's card gate. A workspace with no card can look around and
// set itself up, and nothing else: every other page shows one panel that says
// what to do, instead of pages whose every action would be refused.
import { type ReactNode, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { Page } from "@/components/page"
import { Panel } from "@/components/panel"
import { PageLoading } from "@/components/route-feedback"
import { FieldProblem } from "@/components/settings-section"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useOrganization } from "@/lib/organization"
import { usePath } from "@/lib/router"

import { useBillingStatus, useSetupSession } from "./client"

// Where a workspace without a card can still go: its billing and other
// settings, connecting the providers it will use, its people and keys, the
// person's own account, and the platform pages. Prefix matches, except the
// three listed pages, which have no pages beneath them that should open.
const allowedPrefixes = [
  "/settings/",
  "/integrations",
  "/clouds/accounts/",
  "/account",
  "/notifications",
  "/platform/",
]
const allowedExact = ["/members", "/teams", "/api-keys"]

/** allowedWithoutCard says whether a page opens while the workspace has no card. */
export function allowedWithoutCard(path: string): boolean {
  return (
    allowedExact.includes(path) ||
    allowedPrefixes.some((p) => path.startsWith(p))
  )
}

/** CardGate shows children, or the card panel while the workspace is pending. */
export function CardGate({ children }: { children: ReactNode }) {
  const path = usePath()
  const status = useBillingStatus()
  // A missing status route is another edition's server, and any other failure
  // is no reason to lock people out. Only a payment refusal blocks.
  const refused = !!status.error && statusOf(status.error) === 402
  const pending = status.data?.status === "pending_payment"
  if (!status.data && !status.error) return <PageLoading />
  if ((pending || refused) && !allowedWithoutCard(path)) return <CardPanel />
  return <>{children}</>
}

function CardPanel() {
  const { can } = useOrganization()
  const owner = can("organization:administer")
  const setup = useSetupSession()
  const [problem, setProblem] = useState("")
  const start = async () => {
    setProblem("")
    try {
      const { url } = await setup.mutateAsync()
      window.location.assign(url)
    } catch (err) {
      setProblem(messageOf(err, "Couldn't start adding a card. Try again."))
    }
  }
  return (
    <>
      <SiteHeader crumbs={[{ label: "Add a card" }]} />
      <Page>
        <Panel className="mx-auto mt-10 max-w-lg gap-3 p-6">
          {owner ? (
            <>
              <h1 className="text-lg font-semibold tracking-tight">
                Add a card to start using Loomplane Cloud
              </h1>
              <p className="text-[13.5px] text-muted-foreground">
                $5 a month, including $5 of usage. Nothing runs in this
                workspace until a card is on file. You can still connect clouds
                and invite people.
              </p>
              <div>
                <Button onClick={() => void start()} disabled={setup.isPending}>
                  {setup.isPending && <Spinner />}
                  Add a card
                </Button>
              </div>
              <FieldProblem text={problem} />
            </>
          ) : (
            <>
              <h1 className="text-lg font-semibold tracking-tight">
                This workspace has no card yet
              </h1>
              <p className="text-[13.5px] text-muted-foreground">
                The workspace owner needs to add a card before anything runs
                here.
              </p>
            </>
          )}
        </Panel>
      </Page>
    </>
  )
}
