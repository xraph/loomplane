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
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { clearInvite } from "@/lib/invite"

const lastOrgKey = "loomplane.last-org"

/**
 * AcceptInvitation joins the organization an invitation is for, then makes
 * it the session's organization. It asks first instead of joining on load,
 * so opening a link is never enough to change anything.
 */
export function AcceptInvitation({ token }: { token: string }) {
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
          "That invitation was for a different account. That account has joined; sign in with it to open the workspace."
        )
        return
      }
      await switchOrg.mutateAsync({ body: { orgId: member.orgId } })
      window.localStorage.setItem(lastOrgKey, member.orgId)
      invalidate(useSessionGet)
      // Last: clearing it sends the accept page home, into the workspace.
      clearInvite()
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


  return (
    <AuthLayout title="Join a workspace">
      <AuthCard
        title="Join a workspace"
        description={`You've been invited to a workspace on Loomplane${user?.email ? `, as ${user.email}` : ""}.`}
        align="left"
        variant="borderless"
        className="auth-card"
        footer={
          <p className="text-[13px] text-muted-foreground">
            Wrong account?{" "}
            <button
              type="button"
              className="font-medium text-(--auth-link) underline-offset-4 hover:underline disabled:opacity-50"
              onClick={() => {
                if (spent) clearInvite()
                void signOut()
              }}
              disabled={busy}
            >
              Sign out
            </button>
          </p>
        }
      >
        <div className="flex flex-col gap-2">
          {error && (
            <Alert variant="destructive" className="mb-2">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button onClick={() => void join()} disabled={busy}>
            {busy && <Spinner />}
            Accept invitation
          </Button>
          {/* Declining is the other half of the same choice, so it sits
              beside accepting rather than down in the footer. */}
          <Button variant="ghost" onClick={clearInvite} disabled={busy}>
            Not now
          </Button>
        </div>
      </AuthCard>
    </AuthLayout>
  )
}
