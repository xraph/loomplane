// AI settings: what the AI in every project of the workspace shares. For
// now that's the Hugging Face token gated models download with. It's
// write-only: once saved, only its last characters, the account it belongs
// to and when it was set come back.
import { CheckIcon, LockIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useAiSettingsDeleteHuggingFaceToken,
  useAiSettingsGet,
  useAiSettingsPutHuggingFaceToken,
} from "@/client/forge/loomplane/hooks"
import type { HuggingFaceToken } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ApiKeysIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { formatDate } from "@/lib/format"

const description =
  "Settings the AI in every project of this workspace shares. Models run on your own servers, and Loomplane keeps only what they need to start."

/** AISettings is the workspace's AI settings page. */
export function AISettings() {
  const settings = useQuery(useAiSettingsGet)
  const denied = settings.status === "error" && statusOf(settings.error) === 403
  return (
    <>
      <SiteHeader crumbs={[{ label: "AI settings" }]} />
      <Page className="max-w-[880px]">
        <PageHeading title="AI settings" description={description} />
        {denied && <Restricted />}
        {settings.status === "error" && !denied && (
          <QueryError
            error={settings.error}
            fallback="AI settings couldn't be loaded."
            retry={settings.refetch}
          />
        )}
        {!settings.data && settings.status !== "error" && (
          <Skeleton
            data-testid="ai-settings-loading"
            className="h-56 w-full rounded-[14px]"
          />
        )}
        {settings.data && (
          <HuggingFaceSection
            token={settings.data.huggingface}
            manage={settings.data.canManage}
          />
        )}
      </Page>
    </>
  )
}

function Restricted() {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LockIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>AI settings are restricted</ZeroStateTitle>
        <ZeroStateDescription>
          Ask your workspace owner for access to AI in this workspace.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

function HuggingFaceSection({
  token,
  manage,
}: {
  token?: HuggingFaceToken
  manage: boolean
}) {
  const heading = useId()
  return (
    <Panel aria-labelledby={heading} className="gap-4 px-4 py-4 md:px-5">
      <div className="grid gap-1">
        <h2 id={heading} className="text-[15px] font-semibold tracking-tight">
          Hugging Face token
        </h2>
        <p className="max-w-[64ch] text-[13px] leading-relaxed text-pretty text-muted-foreground">
          Gated or private Hugging Face models download with it. Loomplane keeps
          it encrypted and never shows it again.
        </p>
      </div>
      {token ? <TokenSummary token={token} /> : <NoToken manage={manage} />}
      {manage ? (
        <TokenForm hasToken={!!token} />
      ) : (
        <p className="text-[12.5px] text-muted-foreground">
          Only workspace admins can change the token.
        </p>
      )}
    </Panel>
  )
}

function TokenSummary({ token }: { token: HuggingFaceToken }) {
  return (
    <dl className="grid grid-cols-1 gap-x-6 gap-y-3 rounded-[10px] bg-subtle px-3.5 py-3 sm:grid-cols-3">
      <Detail label="Token">
        <span className="font-mono tabular-nums">•••• {token.hint}</span>
      </Detail>
      <Detail label="Account">
        <span className="break-all">{token.account}</span>
      </Detail>
      <Detail label="Set">{formatDate(token.setAt)}</Detail>
    </dl>
  )
}

function Detail({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <dt className="text-[11.5px] text-muted-foreground">{label}</dt>
      <dd className="text-[13px]">{children}</dd>
    </div>
  )
}

function NoToken({ manage }: { manage: boolean }) {
  return (
    <ZeroState variant="inline" className="rounded-[10px] bg-subtle">
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <ApiKeysIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>No token yet</ZeroStateTitle>
        <ZeroStateDescription>
          {manage
            ? "Public models deploy without one. To deploy a gated model, accept its license on Hugging Face, then add a token here."
            : "Public models deploy without one. Ask a workspace admin to add one before you deploy a gated model."}
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

function TokenForm({ hasToken }: { hasToken: boolean }) {
  const save = useMutation(useAiSettingsPutHuggingFaceToken)
  const remove = useMutation(useAiSettingsDeleteHuggingFaceToken)
  const invalidate = useInvalidate()
  const field = useId()
  const [value, setValue] = useState("")
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState("")
  const [confirming, setConfirming] = useState(false)
  const busy = save.isPending || remove.isPending

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const next = value.trim()
    if (!next) return
    setError("")
    setSaved(false)
    try {
      await save.mutateAsync({ body: { token: next } })
      setSaved(true)
      invalidate(useAiSettingsGet)
    } catch (err) {
      setError(messageOf(err, "The token wasn't saved."))
    } finally {
      // Never left in the page, saved or not.
      setValue("")
    }
  }

  const doRemove = async () => {
    setError("")
    setSaved(false)
    try {
      await remove.mutateAsync({})
      invalidate(useAiSettingsGet)
    } catch (err) {
      setError(messageOf(err, "The token wasn't removed."))
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-3">
      <Field>
        <FieldLabel htmlFor={field}>Token</FieldLabel>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            id={field}
            type="password"
            value={value}
            onChange={(e) => {
              setValue(e.target.value)
              setSaved(false)
            }}
            placeholder="Paste a token with read access"
            autoComplete="new-password"
            spellCheck={false}
            className="min-w-0 flex-1 font-mono text-xs"
          />
          <div className="flex gap-2">
            <Button
              type="submit"
              disabled={busy || !value.trim()}
              className="flex-1 sm:flex-none"
            >
              {save.isPending && <Spinner />}
              Save
            </Button>
            {hasToken && (
              <AlertDialog open={confirming} onOpenChange={setConfirming}>
                <AlertDialogTrigger
                  render={
                    <Button
                      type="button"
                      variant="outline"
                      disabled={busy}
                      className="flex-1 sm:flex-none"
                    />
                  }
                >
                  {remove.isPending && <Spinner />}
                  Remove token
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>
                      Remove the Hugging Face token?
                    </AlertDialogTitle>
                    <AlertDialogDescription>
                      Gated models can&apos;t be deployed until you add another.
                      While a deployed model still downloads with it, Loomplane
                      keeps it and says which.
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>Keep it</AlertDialogCancel>
                    <AlertDialogAction
                      variant="destructive"
                      onClick={() => {
                        // A refusal shows beside the token, not in here.
                        setConfirming(false)
                        void doRemove()
                      }}
                    >
                      Remove
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
          </div>
        </div>
        {hasToken && (
          <FieldDescription>Saving a new one replaces it.</FieldDescription>
        )}
      </Field>
      {saved && (
        <p
          role="status"
          className="flex items-center gap-1.5 text-[13px] text-overdye-ink"
        >
          <CheckIcon className="size-4 shrink-0" />
          Saved. Loomplane never shows this token again.
        </p>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </form>
  )
}
