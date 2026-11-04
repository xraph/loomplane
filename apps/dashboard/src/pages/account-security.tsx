// Your account's security: the password, and the ways you can sign in.
import { useState } from "react"

import { messageOf } from "@/client/errors"
import { AccountLayout } from "@/components/account-layout"
import { ChangePasswordDialog } from "@/components/change-password-dialog"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { SettingsSection } from "@/components/settings-section"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useAccountClient, useLoad } from "@/lib/account"
import { formatDate } from "@/lib/format"

/** AccountSecurity is the Security tab of your account. */
export function AccountSecurity() {
  const { client, token } = useAccountClient()
  const me = useLoad(() => client.getMe(token))
  return (
    <AccountLayout tab="security">
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
      <SignInMethods />
    </AccountLayout>
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
