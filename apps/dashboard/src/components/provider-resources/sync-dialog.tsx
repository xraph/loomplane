import { useState } from "react"
import { messageOf } from "@/client/errors"
import type { Connection } from "@/client/forge/loomplane/types"
import { CloudGlyph } from "@/components/cloud-glyph"
import { StatusPill } from "@/components/runs"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
} from "@/components/zero-state"
import { formatDate } from "@/lib/format"

export type InventorySync = {
  id: string
  connectionId: string
  status: string
  error: string
  startedAt: string
  finishedAt?: string | null
}
export function ResourceSyncDialog({
  accounts,
  syncs,
  open,
  onOpenChange,
  onSync,
  canSync = true,
}: {
  accounts: Pick<Connection, "id" | "name" | "cloud" | "status">[]
  syncs: InventorySync[]
  open: boolean
  onOpenChange: (open: boolean) => void
  canSync?: boolean
  onSync: (connectionId: string) => Promise<unknown>
}) {
  const [pending, setPending] = useState<string[]>([])
  const [errors, setErrors] = useState<Record<string, string>>({})
  const sync = async (id: string) => {
    if (!canSync) return
    setPending((ids) => [...ids, id])
    setErrors((current) => ({ ...current, [id]: "" }))
    try {
      await onSync(id)
    } catch (error) {
      setErrors((current) => ({
        ...current,
        [id]: messageOf(
          error,
          "Sync failed. Check the account's read permissions and retry."
        ),
      }))
    } finally {
      setPending((ids) => ids.filter((value) => value !== id))
    }
  }
  const ready = accounts.filter((account) => account.status === "ready")
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Sync provider resources</DialogTitle>
          <DialogDescription>
            Read inventory from your connected accounts. Sync does not change
            provider resources or retrieve passwords.
          </DialogDescription>
        </DialogHeader>
        <div className="grid max-h-[60vh] gap-2 overflow-y-auto">
          {accounts.length === 0 ? (
            <ZeroState variant="compact" preview={false}>
              <ZeroStateHeader>
                <ZeroStateTitle>No supported accounts</ZeroStateTitle>
                <ZeroStateDescription>
                  Connect an account with an installed inventory reader in
                  Integrations.
                </ZeroStateDescription>
              </ZeroStateHeader>
            </ZeroState>
          ) : (
            accounts.map((account) => {
              const last = syncs
                .filter((s) => s.connectionId === account.id)
                .sort((a, b) => b.startedAt.localeCompare(a.startedAt))[0]
              return (
                <div
                  key={account.id}
                  className="grid gap-1.5 border-b py-2 last:border-0"
                >
                  <div className="flex items-center gap-2">
                    <CloudGlyph cloud={account.cloud} size="sm" />
                    <span className="min-w-0 flex-1 truncate font-medium">
                      {account.name}
                    </span>
                    {last && (
                      <StatusPill
                        tone={
                          last.status === "complete"
                            ? "quiet"
                            : last.status === "running"
                              ? "flight"
                              : "waiting"
                        }
                      >
                        {last.status}
                      </StatusPill>
                    )}
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Sync ${account.name}`}
                      disabled={
                        !canSync ||
                        pending.includes(account.id) ||
                        account.status !== "ready"
                      }
                      onClick={() => void sync(account.id)}
                    >
                      {pending.includes(account.id) ? "Syncing…" : "Sync"}
                    </Button>
                  </div>
                  {last && (
                    <p className="text-xs text-muted-foreground">
                      Last attempt {formatDate(last.startedAt)}
                      {last.error ? `. ${last.error}` : ""}
                    </p>
                  )}
                  {account.status !== "ready" && (
                    <p className="text-xs text-muted-foreground">
                      Check this account in Integrations before syncing.
                    </p>
                  )}
                  {errors[account.id] && (
                    <Alert variant="destructive">
                      <AlertDescription>{errors[account.id]}</AlertDescription>
                    </Alert>
                  )}
                </div>
              )
            })
          )}
        </div>
        {canSync && ready.length > 1 && (
          <Button
            size="sm"
            disabled={pending.length > 0}
            onClick={() =>
              void Promise.all(ready.map((account) => sync(account.id)))
            }
          >
            Sync all ready accounts
          </Button>
        )}
      </DialogContent>
    </Dialog>
  )
}
