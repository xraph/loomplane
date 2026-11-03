// The webhook a GitLab connection listens on, and the secret that signs it.
// The secret is shown once, kept only in this component's state, and gone
// when the panel closes. It never reaches a cache, storage or the URL.
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

export function GitLabWebhook({
  connection,
}: {
  connection: GitLabConnectionView
}) {
  const create = useMutation(useGitlabWebhookSecret)
  const invalidate = useInvalidate()
  const [signingToken, setSigningToken] = useState("")
  const [secret, setSecret] = useState<GitLabWebhookSecret | null>(null)
  const [replacing, setReplacing] = useState(false)
  const url = secret?.url || connection.webhookUrl

  const submit = async () => {
    setReplacing(false)
    const token = signingToken.trim()
    try {
      const made = (await create.mutateAsync({
        path: { id: connection.id },
        body: token ? { signingToken: token } : {},
      })) as GitLabWebhookSecret
      setSecret(made)
      setSigningToken("")
      invalidate(useGitlabStatus)
    } catch (err) {
      toast.error(messageOf(err, "The secret wasn't created."))
    }
  }

  return (
    <div className="grid gap-3 rounded-lg border bg-subtle p-3">
      <p className="text-[12.5px] leading-relaxed text-muted-foreground">
        Add a webhook with Push events on the project or its group, and paste
        this URL and one of these secrets. Use the signing token on GitLab 19
        and newer.
      </p>
      <Value name="Webhook URL" value={url} copy="Copy webhook URL" />
      <div className="flex flex-wrap items-end gap-2">
        <label className="grid min-w-[14rem] flex-1 gap-1">
          <span className="text-[12.5px] text-muted-foreground">
            Paste GitLab&apos;s signing token
          </span>
          <Input
            value={signingToken}
            onChange={(e) => setSigningToken(e.target.value)}
            placeholder="Optional"
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <Button
          type="button"
          variant="outline"
          disabled={create.isPending}
          onClick={() =>
            connection.webhookSet ? setReplacing(true) : void submit()
          }
        >
          {create.isPending && <Spinner />}
          {connection.webhookSet ? "Replace secret" : "Create secret"}
        </Button>
      </div>
      {secret && (
        <div className="grid gap-3" role="status">
          <p className="text-[12.5px] font-medium">
            Copy these now. They aren&apos;t shown again.
          </p>
          <Value
            name="Signing token"
            value={secret.signingToken}
            copy="Copy signing token"
          />
          <Value
            name="Secret token"
            value={secret.secretToken}
            copy="Copy secret token"
          />
        </div>
      )}
      <AlertDialog open={replacing} onOpenChange={setReplacing}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Replace the secret?</AlertDialogTitle>
            <AlertDialogDescription>
              The webhook already set up in GitLab stops working until you paste
              the new secret there.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => void submit()}
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
