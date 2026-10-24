import { useEffect, useRef, useState } from "react"
import { useMutation } from "@/client/hooks"
import { useNativeRecoveryCopiesCredentials } from "@/client/forge/loomplane/hooks"
import type {
  NativeCopyCredentials,
  NativeRestore,
} from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { formatDate } from "@/lib/format"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Alert, AlertDescription } from "@/components/ui/alert"

export function NativeCopyCredentialsControl({
  copy,
}: {
  copy: NativeRestore
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        Reader credentials
      </Button>
      {open && <CredentialsDialog copy={copy} close={() => setOpen(false)} />}
    </>
  )
}

// Mount only while open. The generated mutation has no entity/cache contract;
// closing discards its local response and cancels any pending inspection.
function CredentialsDialog({
  copy,
  close,
}: {
  copy: NativeRestore
  close: () => void
}) {
  const reveal = useMutation(useNativeRecoveryCopiesCredentials)
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  const credentials = reveal.data as NativeCopyCredentials | undefined
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) close()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Recovery copy credentials</DialogTitle>
          <DialogDescription>
            Read the temporary copy with an expiring reader login. Access is
            checked again when you reveal it. You need your own authorized Cloud
            SQL connector identity. An open SQL session can outlive the login
            expiry.
          </DialogDescription>
        </DialogHeader>
        <p className="text-xs break-all text-muted-foreground">
          {copy.id} · Expires {formatDate(copy.expiresAt)}
        </p>
        {reveal.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(
                reveal.error,
                "Reader access could not be checked. Inspect copy status and retry."
              )}
            </AlertDescription>
          </Alert>
        )}
        {credentials && (
          <div className="grid min-w-0 gap-2 text-xs">
            <p className="break-all">
              Connection: {credentials.connectionName}
            </p>
            <p className="break-all">
              Database: {credentials.database} · User: {credentials.username}
            </p>
            <label className="grid gap-1">
              Reader password
              <Input
                aria-label="Reader password"
                type="password"
                readOnly
                autoComplete="off"
                value={credentials.password}
              />
            </label>
            <p className="text-muted-foreground">
              Checked {formatDate(credentials.checkedAt)}. This dialog forgets
              the password when you close it. A password you copy remains usable
              until login expiry or copy removal.
            </p>
          </div>
        )}
        <DialogFooter>
          {!credentials && (
            <Button
              size="sm"
              disabled={reveal.isPending}
              onClick={() => {
                controller.current?.abort()
                controller.current = new AbortController()
                void reveal
                  .mutateAsync(
                    { path: { id: copy.id } },
                    { signal: controller.current.signal }
                  )
                  .catch(() => undefined)
              }}
            >
              {reveal.isPending
                ? "Checking reader access…"
                : "Reveal reader credentials"}
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={close}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
