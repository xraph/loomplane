// The webhook a GitLab connection listens on, and the secret that proves a
// delivery came from that GitLab. GitLab 19 and newer make the signing
// token themselves, so it is pasted here and only goes one way. Older
// versions take a secret token Loomplane makes, shown once, kept only in
// this component's state and gone when the panel closes. It never reaches a
// cache, storage or the URL.
import { type ReactNode, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useGitlabStatus,
  useGitlabWebhookSecret,
} from "@/client/forge/loomplane/hooks"
import type {
  GitLabConnectionView,
  GitLabWebhookSecret,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { CopyButton } from "@/components/keyless-setup"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

/** How the secret is set: GitLab's signing token, or one Loomplane makes. */
type Kind = "signing" | "secret"

export function GitLabWebhook({
  connection,
}: {
  connection: GitLabConnectionView
}) {
  const create = useMutation(useGitlabWebhookSecret)
  const invalidate = useInvalidate()
  const [signingToken, setSigningToken] = useState("")
  const [secretToken, setSecretToken] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [replacing, setReplacing] = useState<Kind | null>(null)
  const [pending, setPending] = useState<Kind | null>(null)
  const [url, setURL] = useState(connection.webhookUrl)
  const token = signingToken.trim()

  const submit = async (kind: Kind) => {
    setReplacing(null)
    setPending(kind)
    try {
      const made = (await create.mutateAsync({
        path: { id: connection.id },
        body: kind === "signing" ? { signingToken: token } : {},
      })) as GitLabWebhookSecret
      if (made.url) setURL(made.url)
      if (kind === "signing") {
        setSigningToken("")
        setSecretToken(null)
        setSaved(true)
      } else {
        setSecretToken(made.secretToken)
        setSaved(false)
      }
      invalidate(useGitlabStatus)
    } catch (err) {
      toast.error(
        messageOf(
          err,
          kind === "signing"
            ? "The signing token wasn't saved."
            : "The secret wasn't created."
        )
      )
    } finally {
      setPending(null)
    }
  }

  // A connection that has a secret asks before replacing it.
  const start = (kind: Kind) =>
    connection.webhookSet ? setReplacing(kind) : void submit(kind)

  return (
    <div className="grid gap-3 rounded-lg border bg-subtle p-3">
      <p className="text-[12.5px] leading-relaxed text-muted-foreground">
        In GitLab, add a webhook with Push events on the project or its group,
        and paste this URL.
      </p>
      <Value name="Webhook URL" value={url} copy="Copy webhook URL" />
      <div className="grid gap-2">
        <p className="text-[12.5px] leading-relaxed text-muted-foreground">
          On GitLab 19 and newer, choose Generate signing token in GitLab, copy
          it and paste it here. Loomplane keeps it.
        </p>
        <div className="flex flex-wrap items-end gap-2">
          <label className="grid min-w-[14rem] flex-1 gap-1">
            <span className="text-[12.5px] text-muted-foreground">
              Paste GitLab&apos;s signing token
            </span>
            <Input
              value={signingToken}
              onChange={(e) => setSigningToken(e.target.value)}
              placeholder="whsec_..."
              autoComplete="off"
              spellCheck={false}
            />
          </label>
          <Button
            type="button"
            variant="outline"
            disabled={create.isPending || !token}
            onClick={() => start("signing")}
          >
            {pending === "signing" && <Spinner />}
            Save signing token
          </Button>
        </div>
        {saved && (
          <p className="text-[12.5px] font-medium" role="status">
            Saved. Loomplane checks GitLab&apos;s signature on every delivery.
          </p>
        )}
      </div>
      <div className="grid gap-2">
        <p className="text-[12.5px] leading-relaxed text-muted-foreground">
          On older GitLab, create a secret here and paste it into GitLab&apos;s
          Secret token field.
        </p>
        <div>
          <Button
            type="button"
            variant="outline"
            disabled={create.isPending}
            onClick={() => start("secret")}
          >
            {pending === "secret" && <Spinner />}
            {connection.webhookSet ? "Replace secret" : "Create secret"}
          </Button>
        </div>
        {secretToken && (
          <div className="grid gap-3" role="status">
            <p className="text-[12.5px] font-medium">
              Copy it now. It isn&apos;t shown again.
            </p>
            <Value
              name="Secret token"
              value={secretToken}
              copy="Copy secret token"
            />
          </div>
        )}
      </div>
      <AlertDialog
        open={replacing !== null}
        onOpenChange={(open) => !open && setReplacing(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Replace the secret?</AlertDialogTitle>
            <AlertDialogDescription>
              Loomplane forgets the current secret, so the webhook already set
              up in GitLab stops working until it uses the new one.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => replacing && void submit(replacing)}
            >
              Replace it
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Value({
  name,
  value,
  copy,
}: {
  name: ReactNode
  value: string
  copy: string
}) {
  return (
    <div className="grid gap-1">
      <span className="text-[12.5px] text-muted-foreground">{name}</span>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto rounded-lg border bg-background px-3 py-1.5 font-mono text-xs whitespace-nowrap">
          {value}
        </code>
        <CopyButton value={value} label={copy} />
      </div>
    </div>
  )
}
