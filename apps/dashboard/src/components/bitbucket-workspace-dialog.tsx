// Connect a Bitbucket workspace: ask which one, then leave for Bitbucket,
// where the person approves Loomplane's OAuth consumer and is sent back to
// the callback page.
import { type FormEvent, useState } from "react"

import { messageOf } from "@/client/errors"
import { useBitbucketAuthorize } from "@/client/forge/loomplane/hooks"
import type { BitbucketAuthorization } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
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
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

/**
 * useBitbucketAuthorize asks for the link that connects a workspace and
 * leaves for it. It resolves to the message that says why not, or nothing.
 */
export function useBitbucketSignIn() {
  const authorize = useMutation(useBitbucketAuthorize)
  const signIn = async (workspace: string): Promise<string | undefined> => {
    try {
      const link = (await authorize.mutateAsync({
        body: { workspace },
      })) as BitbucketAuthorization
      window.location.assign(link.url)
    } catch (err) {
      return messageOf(err, "Bitbucket couldn't be reached.")
    }
  }
  return { signIn, pending: authorize.isPending }
}

/** slugOf is the workspace a person typed or pasted from bitbucket.org/acme. */
function slugOf(typed: string): string {
  return typed
    .trim()
    .replace(/^(https?:\/\/)?(www\.)?bitbucket\.org\//i, "")
    .split("/")[0]
}

export function BitbucketWorkspaceDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Connect a Bitbucket workspace</DialogTitle>
          <DialogDescription>
            Bitbucket asks you to approve Loomplane, then sends you back here.
          </DialogDescription>
        </DialogHeader>
        {/* Mounted only while open, so it starts empty each time. */}
        <WorkspaceForm onCancel={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function WorkspaceForm({ onCancel }: { onCancel: () => void }) {
  const { signIn, pending } = useBitbucketSignIn()
  const [typed, setTyped] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [leaving, setLeaving] = useState(false)
  const workspace = slugOf(typed)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!workspace || pending) return
    setError(null)
    const failure = await signIn(workspace)
    if (failure) {
      setError(failure)
    } else {
      setLeaving(true)
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-4">
      <Field>
        <FieldLabel htmlFor="bitbucket-workspace">Workspace slug</FieldLabel>
        <Input
          id="bitbucket-workspace"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder="acme"
          className="font-mono"
          autoComplete="off"
          autoFocus
          spellCheck={false}
        />
        <FieldDescription>The acme in bitbucket.org/acme.</FieldDescription>
      </Field>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={!workspace || pending || leaving}>
          {(pending || leaving) && <Spinner />}
          Connect workspace
        </Button>
      </DialogFooter>
    </form>
  )
}
