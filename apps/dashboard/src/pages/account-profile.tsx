// Your profile: the name teammates see, your email, a copy of your data,
// and closing the account. authsome keeps all of it; the dashboard only
// asks.
import { useAuth } from "@authsome/ui-react"
import { CheckIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf } from "@/client/errors"
import { AccountLayout } from "@/components/account-layout"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import {
  FieldProblem,
  SettingsCardFooter,
  SettingsSection,
} from "@/components/settings-section"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useAccountClient, useLoad } from "@/lib/account"

type Me = Awaited<
  ReturnType<ReturnType<typeof useAccountClient>["client"]["getMe"]>
>

/** AccountProfile is the Profile tab of your account. */
export function AccountProfile() {
  const { client, token } = useAccountClient()
  const me = useLoad(() => client.getMe(token))
  return (
    <AccountLayout tab="profile">
      {me.status === "error" && (
        <QueryError
          error={me.error}
          fallback="Your profile couldn't be loaded."
          retry={me.reload}
        />
      )}
      {me.status === "loading" && (
        <Skeleton className="h-64 w-full rounded-[14px]" />
      )}
      {me.data && (
        <>
          <ProfileForm me={me.data} onSaved={me.reload} />
          <EmailSection me={me.data} />
          <DataSection email={me.data.email} />
        </>
      )}
    </AccountLayout>
  )
}

function initials(me: Me) {
  const name = `${me.first_name} ${me.last_name}`.trim() || me.email
  return (
    name
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join("") || "?"
  )
}

function ProfileForm({
  me,
  onSaved,
}: {
  me: Me
  onSaved: () => Promise<void>
}) {
  const { client, token } = useAccountClient()
  const { manager } = useAuth()
  const ids = { first: useId(), last: useId(), username: useId() }
  const [first, setFirst] = useState(me.first_name)
  const [last, setLast] = useState(me.last_name)
  const [username, setUsername] = useState(me.username ?? "")
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [problem, setProblem] = useState("")

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setSaved(false)
    setProblem("")
    try {
      await client.updateMe(
        {
          first_name: first.trim(),
          last_name: last.trim(),
          username: username.trim(),
        },
        token
      )
      // The sidebar's name comes from the session's user.
      await manager.refreshNow()
      await onSaved()
      setSaved(true)
    } catch (err) {
      setProblem(messageOf(err, "Your profile wasn't saved."))
    } finally {
      setSaving(false)
    }
  }

  const changed = () => setSaved(false)
  return (
    <SettingsSection
      title="Profile"
      description="How teammates see you in approvals, activity and comments."
      className="border-t-0 pt-0"
    >
      <Panel className="gap-0 p-0">
        <form onSubmit={(e) => void save(e)} className="grid">
          <div className="grid gap-4 px-4 py-4 md:px-5">
            <Avatar className="size-14">
              {me.image && <AvatarImage src={me.image} alt="" />}
              <AvatarFallback className="text-base font-semibold">
                {initials(me)}
              </AvatarFallback>
            </Avatar>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field>
                <FieldLabel htmlFor={ids.first}>First name</FieldLabel>
                <Input
                  id={ids.first}
                  value={first}
                  autoComplete="given-name"
                  onChange={(e) => {
                    setFirst(e.target.value)
                    changed()
                  }}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor={ids.last}>Last name</FieldLabel>
                <Input
                  id={ids.last}
                  value={last}
                  autoComplete="family-name"
                  onChange={(e) => {
                    setLast(e.target.value)
                    changed()
                  }}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor={ids.username}>Username</FieldLabel>
                <Input
                  id={ids.username}
                  value={username}
                  autoComplete="username"
                  spellCheck={false}
                  className="font-mono text-xs"
                  onChange={(e) => {
                    setUsername(e.target.value)
                    changed()
                  }}
                />
              </Field>
            </div>
            <FieldProblem text={problem} />
          </div>
          <SettingsCardFooter>
            {saved ? (
              <p
                role="status"
                className="flex items-center gap-1.5 text-[12.5px] text-overdye-ink"
              >
                <CheckIcon className="size-4 shrink-0" />
                Saved.
              </p>
            ) : (
              <p className="text-[12.5px] text-muted-foreground">
                Your name shows in every workspace you belong to.
              </p>
            )}
            <Button type="submit" size="sm" disabled={saving}>
              {saving && <Spinner />}
              Save profile
            </Button>
          </SettingsCardFooter>
        </form>
      </Panel>
    </SettingsSection>
  )
}

function EmailSection({ me }: { me: Me }) {
  return (
    <SettingsSection
      title="Email"
      description="Where sign-in links, invitations and deployment results go."
    >
      <Panel className="flex-row flex-wrap items-center justify-between gap-3 px-4 py-3.5 md:px-5">
        <span className="flex flex-wrap items-center gap-2.5">
          <span className="text-[14px] font-medium">{me.email}</span>
          {me.email_verified ? (
            <Badge variant="secondary" className="gap-1">
              <CheckIcon className="size-3" />
              Verified
            </Badge>
          ) : (
            <Badge variant="outline">Not verified</Badge>
          )}
        </span>
      </Panel>
    </SettingsSection>
  )
}

function DataSection({ email }: { email: string }) {
  const { client, token } = useAccountClient()
  const { signOut } = useAuth()
  const field = useId()
  const [exporting, setExporting] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [typed, setTyped] = useState("")
  const [problem, setProblem] = useState("")

  const download = async () => {
    setExporting(true)
    setProblem("")
    try {
      const data = await client.exportMyData(token)
      const url = URL.createObjectURL(
        new Blob([JSON.stringify(data, null, 2)], {
          type: "application/json",
        })
      )
      const a = document.createElement("a")
      a.href = url
      a.download = "loomplane-account.json"
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      setProblem(messageOf(err, "Your data couldn't be downloaded."))
    } finally {
      setExporting(false)
    }
  }

  const remove = async (e: FormEvent) => {
    e.preventDefault()
    setDeleting(true)
    setProblem("")
    try {
      await client.deleteMe(token)
      // A full load, like signing out from the account menu.
      await signOut()
      window.location.href = "/sign-in"
    } catch (err) {
      setProblem(messageOf(err, "Your account wasn't deleted."))
      setDeleting(false)
    }
  }

  const matches = typed.trim().toLowerCase() === email.toLowerCase()
  return (
    <SettingsSection
      title="Your data"
      description="Take a copy of everything Loomplane holds about you, or close your account."
    >
      <Panel className="flex-row flex-wrap items-center justify-between gap-3 px-4 py-3.5 md:px-5">
        <span className="grid gap-0.5">
          <span className="text-[13.5px] font-semibold">
            Download your data
          </span>
          <span className="text-[12.5px] text-muted-foreground">
            Your profile, sessions and memberships, as JSON.
          </span>
        </span>
        <Button
          variant="outline"
          size="sm"
          disabled={exporting}
          onClick={() => void download()}
        >
          {exporting && <Spinner />}
          Download
        </Button>
      </Panel>
      <Panel className="gap-0 p-0 ring-destructive/30">
        <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5 md:px-5">
          <span className="grid max-w-[60ch] gap-0.5">
            <span className="text-[13.5px] font-semibold text-destructive">
              Delete your account
            </span>
            <span className="text-[12.5px] leading-normal text-muted-foreground">
              You leave every workspace and can&apos;t sign in again. A
              workspace you own needs another owner first.
            </span>
          </span>
          {!confirming && (
            <Button
              variant="outline"
              size="sm"
              className="text-destructive"
              onClick={() => setConfirming(true)}
            >
              Delete account
            </Button>
          )}
        </div>
        {confirming && (
          <form
            onSubmit={(e) => void remove(e)}
            className="grid gap-3 rounded-b-[14px] border-t border-border bg-destructive/5 px-4 py-3.5 md:px-5"
          >
            <Field className="max-w-[360px]">
              <FieldLabel htmlFor={field}>
                Type your email to confirm
              </FieldLabel>
              <Input
                id={field}
                type="email"
                value={typed}
                placeholder={email}
                autoComplete="off"
                onChange={(e) => setTyped(e.target.value)}
              />
            </Field>
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  setConfirming(false)
                  setTyped("")
                }}
              >
                Keep my account
              </Button>
              <Button
                type="submit"
                variant="destructive"
                size="sm"
                disabled={!matches || deleting}
              >
                {deleting && <Spinner />}
                Delete for good
              </Button>
            </div>
          </form>
        )}
      </Panel>
      {problem && (
        <Alert variant="destructive">
          <AlertDescription>{problem}</AlertDescription>
        </Alert>
      )}
    </SettingsSection>
  )
}
