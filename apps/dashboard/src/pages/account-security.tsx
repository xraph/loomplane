// Your account's security: the password, two-step verification, passkeys
// and the ways you can sign in. Two-step and passkeys show only when the
// instance runs them (authsome's client config says).
import { useAuth } from "@authsome/ui-react"
import { KeyRoundIcon, ShieldCheckIcon, SmartphoneIcon } from "lucide-react"
import { type ReactNode, useState } from "react"

import { messageOf } from "@/client/errors"
import { useAccountTwoStepGet } from "@/client/forge/loomplane/hooks"
import { useInvalidate, useQuery } from "@/client/hooks"
import { AccountLayout } from "@/components/account-layout"
import { AddPasskeyDialog } from "@/components/add-passkey-dialog"
import { ChangePasswordDialog } from "@/components/change-password-dialog"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { FieldProblem, SettingsSection } from "@/components/settings-section"
import {
  NewRecoveryCodes,
  SetUpTwoStep,
  TurnOffTwoStep,
} from "@/components/two-step"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { passkeysWork, useAccountClient, useLoad } from "@/lib/account"
import { formatDate } from "@/lib/format"
import { cn } from "@/lib/utils"

/** AccountSecurity is the Security tab of your account. */
export function AccountSecurity() {
  const { clientConfig } = useAuth()
  const twoStep = clientConfig?.mfa?.enabled === true
  const passkeys = clientConfig?.passkey?.enabled === true
  return (
    <AccountLayout tab="security">
      {twoStep || passkeys ? (
        <Protection
          twoStep={twoStep}
          passkeys={passkeys}
          password={<PasswordSection />}
        />
      ) : (
        <PasswordSection />
      )}
      <SignInMethods />
    </AccountLayout>
  )
}

function PasswordSection() {
  const { client, token } = useAccountClient()
  const me = useLoad(() => client.getMe(token))
  return (
    <SettingsSection
      title="Password"
      description="Changing it can sign you out everywhere else too."
      className="border-t-0 pt-0"
    >
      <Panel className="flex-row flex-wrap items-center justify-between gap-3 px-4 py-3.5 md:px-5">
        <span className="grid gap-0.5">
          <span
            aria-hidden="true"
            className="font-mono text-[14px] tracking-[0.15em]"
          >
            ••••••••••••
          </span>
          <span className="text-[12.5px] text-muted-foreground">
            {me.data?.password_changed_at
              ? `Last changed ${formatDate(me.data.password_changed_at)}`
              : "Set when you signed up"}
          </span>
        </span>
        <ChangePasswordDialog onChanged={() => void me.reload()} />
      </Panel>
    </SettingsSection>
  )
}

type Passkey = { id: string; name: string; createdAt: string }

/**
 * Protection is the password with two-step and passkeys under it, and a
 * line at the top on how well the account is guarded.
 */
function Protection({
  twoStep,
  passkeys,
  password,
}: {
  twoStep: boolean
  passkeys: boolean
  password: ReactNode
}) {
  const { client, token } = useAccountClient()
  const invalidate = useInvalidate()
  const status = useQuery(useAccountTwoStepGet)
  const keys = useLoad(async () =>
    passkeys
      ? (await client.listPasskeys(token)).credentials.map((c): Passkey => ({
          id: c.id,
          name: c.display_name || "Passkey",
          createdAt: c.created_at,
        }))
      : []
  )
  const on = status.data?.enabled === true
  const count = keys.data?.length ?? 0
  const changed = () => invalidate(useAccountTwoStepGet)

  return (
    <>
      {status.data && keys.data && (
        <Posture twoStep={twoStep && on} passkeys={count} />
      )}
      {password}
      {twoStep && (
        <TwoStepSection
          on={on}
          loading={!status.data && !status.error}
          error={status.error}
          retry={status.refetch}
          onChanged={changed}
        />
      )}
      {passkeys && (
        <PasskeysSection
          list={keys}
          twoStep={twoStep && on}
          onChanged={() => void keys.reload()}
        />
      )}
    </>
  )
}

function Posture({
  twoStep,
  passkeys,
}: {
  twoStep: boolean
  passkeys: number
}) {
  const [title, body] =
    twoStep && passkeys > 0
      ? [
          "Your account is well protected",
          "Two-step verification and a passkey are both set up.",
        ]
      : twoStep
        ? [
            "Two-step verification is on",
            "Add a passkey too, so you can sign in without typing anything.",
          ]
        : passkeys > 0
          ? [
              "You can sign in with a passkey",
              "Your password still works on its own. Turn on two-step verification to guard it.",
            ]
          : [
              "Your password is the only thing guarding your account",
              "Turn on two-step verification. Anyone deploying to production should.",
            ]
  return (
    <div className="flex items-center gap-3.5 rounded-[14px] bg-subtle px-4 py-3 ring-1 ring-border">
      <ShieldCheckIcon aria-hidden="true" className="size-5 flex-none" />
      <span className="grid gap-0.5">
        <span className="text-[13.5px] font-semibold">{title}</span>
        <span className="text-[12.5px] text-muted-foreground">{body}</span>
      </span>
    </div>
  )
}

function TwoStepSection({
  on,
  loading,
  error,
  retry,
  onChanged,
}: {
  on: boolean
  loading: boolean
  error: unknown
  retry: () => Promise<unknown>
  onChanged: () => void
}) {
  return (
    <SettingsSection
      title="Two-step verification"
      description="After your password, Loomplane asks for a code from an app on your phone."
    >
      {error ? (
        <QueryError
          error={error}
          fallback="Whether two-step verification is on couldn't be loaded."
          retry={retry}
        />
      ) : loading ? (
        <Skeleton className="h-[68px] w-full rounded-[14px]" />
      ) : (
        <Panel className="p-0">
          <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5 md:px-5">
            <span className="flex items-center gap-3">
              <span
                aria-hidden="true"
                className="grid size-9 place-items-center rounded-[10px] bg-muted"
              >
                <SmartphoneIcon className="size-[18px]" />
              </span>
              <span className="grid gap-0.5">
                <span className="flex items-center gap-2 text-[13.5px] font-semibold">
                  Authenticator app
                  <span
                    className={cn(
                      "rounded-full px-2 py-px text-[11.5px] font-medium",
                      on
                        ? "bg-overdye-tint text-overdye-ink"
                        : "bg-muted text-muted-foreground"
                    )}
                  >
                    {on ? "On" : "Off"}
                  </span>
                </span>
                <span className="text-[12.5px] text-muted-foreground">
                  1Password, Google Authenticator, Authy or any app that makes
                  6-digit codes.
                </span>
              </span>
            </span>
            {on ? (
              <TurnOffTwoStep onChanged={onChanged} />
            ) : (
              <SetUpTwoStep onChanged={onChanged} />
            )}
          </div>
          {on && (
            <div className="flex flex-wrap items-center justify-between gap-2.5 border-t border-border bg-subtle px-4 py-3 md:px-5">
              <span className="grid gap-0.5">
                <span className="text-[13px] font-medium">Recovery codes</span>
                <span className="text-[12.5px] text-muted-foreground">
                  Each one works once if you lose your phone.
                </span>
              </span>
              <NewRecoveryCodes />
            </div>
          )}
        </Panel>
      )}
    </SettingsSection>
  )
}

function PasskeysSection({
  list,
  twoStep,
  onChanged,
}: {
  list: ReturnType<typeof useLoad<Passkey[]>>
  twoStep: boolean
  onChanged: () => void
}) {
  const { client, token } = useAccountClient()
  const [busy, setBusy] = useState("")
  const [problem, setProblem] = useState("")

  const remove = async (id: string) => {
    setBusy(id)
    setProblem("")
    try {
      await client.deletePasskey(id, token)
      onChanged()
    } catch (err) {
      setProblem(messageOf(err, "That passkey wasn't removed."))
    } finally {
      setBusy("")
    }
  }

  return (
    <SettingsSection
      title="Passkeys"
      description="Sign in with Touch ID, Windows Hello or a security key. Nothing to type, nothing to phish."
    >
      {list.status === "error" && (
        <QueryError
          error={list.error}
          fallback="Your passkeys couldn't be loaded."
          retry={list.reload}
        />
      )}
      {list.status === "loading" && (
        <Skeleton className="h-24 w-full rounded-[14px]" />
      )}
      {list.data && (
        <Panel className="p-0">
          {list.data.length === 0 ? (
            <p className="px-4 py-3.5 text-[13px] text-muted-foreground md:px-5">
              No passkeys yet.
            </p>
          ) : (
            <ul className="divide-y divide-border">
              {list.data.map((k) => (
                <li
                  key={k.id}
                  className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 md:px-5"
                >
                  <span className="flex min-w-0 items-center gap-3">
                    <span
                      aria-hidden="true"
                      className="grid size-9 flex-none place-items-center rounded-[10px] bg-muted"
                    >
                      <KeyRoundIcon className="size-[18px]" />
                    </span>
                    <span className="grid min-w-0 gap-0.5">
                      <span className="truncate text-[13.5px] font-medium">
                        {k.name}
                      </span>
                      <span className="text-[12.5px] text-muted-foreground">
                        {k.createdAt
                          ? `Added ${formatDate(k.createdAt)}`
                          : "Added"}
                      </span>
                    </span>
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={busy !== ""}
                    onClick={() => void remove(k.id)}
                  >
                    {busy === k.id && <Spinner />}
                    Remove
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {passkeysWork() ? (
            <div className="flex justify-end border-t border-border bg-subtle px-4 py-2.5 md:px-5">
              <AddPasskeyDialog twoStep={twoStep} onAdded={onChanged} />
            </div>
          ) : (
            <p className="border-t border-border bg-subtle px-4 py-2.5 text-[12.5px] text-muted-foreground md:px-5">
              This browser can&apos;t make passkeys.
            </p>
          )}
        </Panel>
      )}
      <FieldProblem text={problem} />
    </SettingsSection>
  )
}

type Method = { provider: string; type: string; label: string }

const names: Record<string, string> = {
  password: "Email and password",
  github: "GitHub",
  google: "Google",
  gitlab: "GitLab",
  microsoft: "Microsoft",
}

function SignInMethods() {
  const { client, token } = useAccountClient()
  const list = useLoad(
    async () => (await client.listAuthMethods(token)).methods
  )
  const [busy, setBusy] = useState("")
  const [problem, setProblem] = useState("")

  const unlink = async (provider: string) => {
    setBusy(provider)
    setProblem("")
    try {
      await client.unlinkAuthMethod(provider, token)
      await list.reload()
    } catch (err) {
      setProblem(messageOf(err, "That sign-in method wasn't removed."))
    } finally {
      setBusy("")
    }
  }

  const methods: Method[] = list.data ?? []
  // The last way in can't go: without it you couldn't sign in at all.
  const last = methods.length <= 1
  return (
    <SettingsSection
      title="Sign-in methods"
      description="Accounts you can sign in with. Keep at least one."
    >
      {list.status === "error" && (
        <QueryError
          error={list.error}
          fallback="Your sign-in methods couldn't be loaded."
          retry={list.reload}
        />
      )}
      {list.status === "loading" && (
        <Skeleton className="h-24 w-full rounded-[14px]" />
      )}
      {list.data && (
        <Panel className="p-0">
          <ul className="divide-y divide-border">
            {methods.map((m) => (
              <li
                key={m.provider}
                className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 md:px-5"
              >
                <span className="text-[13.5px] font-medium">
                  {names[m.provider] ?? m.label ?? m.provider}
                </span>
                {m.provider === "password" ? (
                  <span className="text-[12.5px] text-muted-foreground">
                    Primary
                  </span>
                ) : (
                  !last && (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={busy !== ""}
                      onClick={() => void unlink(m.provider)}
                    >
                      {busy === m.provider && <Spinner />}
                      Disconnect
                    </Button>
                  )
                )}
              </li>
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
  )
}
