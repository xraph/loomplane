import { useAuth } from "@authsome/ui-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useAcceptInvitation,
  useSwitchOrg,
} from "@/client/forge/authsome/hooks"
import { useSessionGet } from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { BrandLogo } from "@/components/brand-logo"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
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
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted p-6 md:p-10">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <BrandLogo className="w-36 self-center" />
        <Card>
          <CardHeader className="text-center">
            <CardTitle className="text-xl">Join an organization</CardTitle>
            <CardDescription>
              You've been invited to an organization on Loomplane
              {user?.email ? `, as ${user.email}` : ""}.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <Button onClick={() => void join()} disabled={busy}>
              {busy && <Spinner />}
              Accept invitation
            </Button>
          </CardContent>
          <CardFooter className="justify-center gap-4">
            <Button variant="link" onClick={skip} disabled={busy}>
              Not now
            </Button>
            <Button
              variant="link"
              onClick={() => {
                if (spent) clearInvite()
                void signOut()
              }}
              disabled={busy}
            >
              Sign out
            </Button>
          </CardFooter>
        </Card>
      </div>
    </div>
  )
}
