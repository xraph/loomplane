// Add a self-managed GitLab: the OAuth application someone made on their own
// GitLab, which Loomplane signs people in through.
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useGitlabAddHost,
  useGitlabStatus,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { CopyButton } from "@/components/keyless-setup"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

export function GitLabHostDialog({
  open,
  onOpenChange,
  callbackUrl,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  callbackUrl: string
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add a self-managed GitLab</DialogTitle>
          <DialogDescription>
            Create an application on your GitLab with this callback URL and only
            the read_api scope.
          </DialogDescription>
        </DialogHeader>
        {/* Mounted only while open, so a closed dialog keeps no secret. */}
        <HostForm
          callbackUrl={callbackUrl}
          onDone={() => onOpenChange(false)}
          onCancel={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  )
}

function HostForm({
  callbackUrl,
  onDone,
  onCancel,
}: {
  callbackUrl: string
  onDone: () => void
  onCancel: () => void
}) {
  const add = useMutation(useGitlabAddHost)
  const invalidate = useInvalidate()
  const [webUrl, setWebUrl] = useState("")
  const [clientId, setClientId] = useState("")
  const [clientSecret, setClientSecret] = useState("")
  const [error, setError] = useState<string | null>(null)
  const ready = !!webUrl.trim() && !!clientId.trim() && !!clientSecret.trim()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready || add.isPending) return
    setError(null)
    try {
      await add.mutateAsync({
        body: {
          webUrl: webUrl.trim(),
          clientId: clientId.trim(),
          clientSecret: clientSecret.trim(),
        },
      })
      invalidate(useGitlabStatus)
      toast.success("GitLab added")
      onDone()
    } catch (err) {
      setError(messageOf(err, "That GitLab couldn't be added."))
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-4">
      <div className="grid gap-1.5">
        <span className="text-[12.5px] text-muted-foreground">
          Callback URL
        </span>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 overflow-x-auto rounded-lg border bg-muted/50 px-3 py-1.5 font-mono text-xs whitespace-nowrap">
            {callbackUrl}
          </code>
          <CopyButton value={callbackUrl} label="Copy callback URL" />
        </div>
      </div>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="gitlab-url">GitLab URL</FieldLabel>
          <Input
            id="gitlab-url"
            type="url"
            placeholder="https://gitlab.example.com"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            autoComplete="off"
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="gitlab-client-id">Application ID</FieldLabel>
          <Input
            id="gitlab-client-id"
            value={clientId}
            onChange={(e) => setClientId(e.target.value)}
            autoComplete="off"
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="gitlab-client-secret">Secret</FieldLabel>
          <Input
            id="gitlab-client-secret"
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            autoComplete="off"
          />
        </Field>
      </FieldGroup>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={!ready || add.isPending}>
          {add.isPending && <Spinner />}
          Add GitLab
        </Button>
      </DialogFooter>
    </form>
  )
}
