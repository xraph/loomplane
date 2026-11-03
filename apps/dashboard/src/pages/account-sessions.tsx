// Where you're signed in. The browser you're reading this in is marked and
// can't be signed out from here; signing out of it is the account menu's
// job.
import { LaptopIcon, SmartphoneIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import { AccountLayout } from "@/components/account-layout"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { SettingsSection } from "@/components/settings-section"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  type AccountSession,
  describeAgent,
  toSession,
  useAccountClient,
  useLoad,
} from "@/lib/account"
import { formatDate, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useNow } from "@/lib/use-runs"

/** AccountSessions is the Sessions tab of your account. */
export function AccountSessions() {
  const { client, token } = useAccountClient()
  const { session } = useOrganization()
  const list = useLoad(async () => {
    const out = await client.listSessions(token, 100)
    return out.sessions.map(toSession)
  })
  const [gone, setGone] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState("")
  const [problem, setProblem] = useState("")

  const here = session.sessionId ?? ""
  const shown = (list.data ?? [])
    .filter((s) => !gone.has(s.id))
    // This browser first, then the most recently used.
    .sort((a, b) =>
      a.id === here
        ? -1
        : b.id === here
          ? 1
          : b.lastActivityAt.localeCompare(a.lastActivityAt)
    )
  const others = shown.filter((s) => s.id !== here)

  const signOut = async (id: string) => {
    setBusy(id)
    setProblem("")
    try {
      await client.revokeSession(id, token)
      setGone((g) => new Set(g).add(id))
    } catch (err) {
      setProblem(messageOf(err, "That session wasn't signed out."))
    } finally {
      setBusy("")
    }
  }

  const signOutOthers = async () => {
    setBusy("others")
    setProblem("")
    try {
      await client.revokeOtherSessions(token)
      setGone(new Set(others.map((s) => s.id)))
    } catch (err) {
      setProblem(messageOf(err, "The other sessions weren't signed out."))
    } finally {
      setBusy("")
    }
  }

  return (
    <AccountLayout tab="sessions">
      <SettingsSection
        title="Where you're signed in"
        description="Sign out of anything you don't recognise, then change your password. API keys aren't sessions: revoke those in Settings, API keys."
        className="border-t-0 pt-0"
        aside={
          <Button
            variant="outline"
            size="sm"
            className="mt-2 justify-self-start text-destructive"
            disabled={others.length === 0 || busy !== ""}
            onClick={() => void signOutOthers()}
          >
            {busy === "others" && <Spinner />}
            Sign out everywhere else
          </Button>
        }
      >
        {list.status === "error" && (
          <QueryError
            error={list.error}
            fallback="Your sessions couldn't be loaded."
            retry={list.reload}
          />
        )}
        {list.status === "loading" && (
          <Skeleton className="h-48 w-full rounded-[14px]" />
        )}
        {list.data && (
          <Panel className="p-0">
            <ul aria-label="Sessions" className="divide-y divide-border">
              {shown.map((s) => (
                <SessionRow
                  key={s.id}
                  session={s}
                  current={s.id === here}
                  busy={busy === s.id}
                  // Without knowing which row is this browser, a row's
                  // button could sign you out here. Signing out everywhere
                  // else is still safe: the server keeps this session.
                  onSignOut={here ? () => void signOut(s.id) : undefined}
                />
              ))}
            </ul>
          </Panel>
        )}
        {problem && (
          <Alert variant="destructive">
            <AlertDescription>{problem}</AlertDescription>
          </Alert>
        )}
      </SettingsSection>
    </AccountLayout>
  )
}

function SessionRow({
  session,
  current,
  busy,
  onSignOut,
}: {
  session: AccountSession
  current: boolean
  busy: boolean
  onSignOut?: () => void
}) {
  const now = useNow(60_000)
  const device = describeAgent(session.userAgent)
  const Icon = device.phone ? SmartphoneIcon : LaptopIcon
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 md:px-5">
      <span
        aria-hidden="true"
        className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-muted text-muted-foreground"
      >
        <Icon className="size-4" />
      </span>
      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="flex flex-wrap items-center gap-2 text-[13.5px] font-medium">
          {device.label}
          {current && <Badge variant="secondary">This device</Badge>}
        </span>
        <span className="text-[12px] text-muted-foreground">
          {session.ipAddress && (
            <span className="font-mono">{session.ipAddress} · </span>
          )}
          {session.createdAt && <>Signed in {formatDate(session.createdAt)}</>}
        </span>
      </span>
      <span className="text-[12.5px] text-muted-foreground">
        {current
          ? "Active now"
          : session.lastActivityAt
            ? `Active ${relative(Date.parse(session.lastActivityAt), now)}`
            : ""}
      </span>
      {!current && onSignOut && (
        <Button variant="outline" size="sm" disabled={busy} onClick={onSignOut}>
          {busy && <Spinner />}
          Sign out
        </Button>
      )}
    </li>
  )
}
