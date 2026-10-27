import { AuthCard } from "@authsome/ui-components"
import { useAuth } from "@authsome/ui-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useAcceptInvitation,
  useSwitchOrg,
} from "@/client/forge/authsome/hooks"
import { useSessionGet } from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { AuthLayout } from "@/components/auth-layout"
import { BrandLogo } from "@/components/brand-logo"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { clearInvite } from "@/lib/invite"
import { useNavigate } from "@/lib/router"

const lastOrgKey = "loomplane.last-org"

/**
 * AcceptInvitation joins the organization an invitation is for, then makes
 * it the session's organization. It asks first instead of joining on load,
 * so opening a link is never enough to change anything.
 */
export function AcceptInvitation({ token }: { token: string }) {
  const navigate = useNavigate()
  const { user, signOut } = useAuth()
  const accept = useMutation(useAcceptInvitation)
  const switchOrg = useMutation(useSwitchOrg)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")
  // The token is spent once authsome accepts it, even when it went to
  // another account, so signing out then shouldn't keep offering it.
  const [spent, setSpent] = useState(false)
  const busy = accept.isPending || switchOrg.isPending

  const join = async () => {
    setError("")
    try {
      const member = await accept.mutateAsync({ body: { token } })
      setSpent(true)
      // authsome adds the account the invitation was addressed to, which
      // isn't necessarily the one signed in here.
      if (user && member.userId !== user.id) {
        setError(
          "That invitation was for a different account. That account has joined; sign in with it to open the organization."
        )
        return
      }
      await switchOrg.mutateAsync({ body: { orgId: member.orgId } })
      window.localStorage.setItem(lastOrgKey, member.orgId)
      invalidate(useSessionGet)
      // Last: clearing it takes this page away and shows the organization.
      clearInvite()
      void navigate({ to: "/", replace: true })
    } catch (err) {
      setError(
        /not found for email/i.test(String(err))
          ? "This invitation is for an email address that has no Loomplane account yet. Sign out, then create an account with the address the invitation went to."
          : messageOf(
              err,
              "The invitation couldn't be accepted. It may have expired or been used already."
            )
      )
    }
  }

  const skip = () => {
    clearInvite()
    void navigate({ to: "/", replace: true })
  }

  return (
    <AuthLayout title="Join an organization">
      <AuthCard
        title="Join an organization"
        description={`You've been invited to an organization on Loomplane${user?.email ? `, as ${user.email}` : ""}.`}
        logo={<BrandLogo />}
        align="left"
        variant="borderless"
        className="auth-card"
        footer={
          <div className="flex gap-4">
            <Button
              variant="link"
              className="h-8 px-0 text-foreground"
              onClick={skip}
              disabled={busy}
            >
              Not now
            </Button>
            <Button
              variant="link"
              className="h-8 px-0 text-foreground"
              onClick={() => {
                if (spent) clearInvite()
                void signOut()
              }}
              disabled={busy}
            >
              Sign out
            </Button>
          </div>
        }
      >
        <div className="flex flex-col gap-4">
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button onClick={() => void join()} disabled={busy}>
            {busy && <Spinner />}
            Accept invitation
          </Button>
        </div>
      </AuthCard>
    </AuthLayout>
  )
}
