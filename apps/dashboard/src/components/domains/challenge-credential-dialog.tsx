// Attaching the zone-scoped Cloudflare token that proves wildcard
// certificates through DNS. Not in the mock, whose *.acme.dev row assumes
// the token is already there.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDnsZonesGet,
  useDnsZonesSetChallengeCredential,
  useDomainsOverview,
  useDomainsProject,
} from "@/client/forge/loomplane/hooks"
import type { Connection, Zone } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ConnectDialog } from "@/components/cloud-connections"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"

// Ready Cloudflare connections on the zone's account, other than the
// zone's own. The server checks the rest: one zone, DNS Edit, no registrar.
function candidates(connections: Connection[], zone: Zone): Connection[] {
  return connections.filter(
    (c) =>
      c.cloud === "cloudflare" &&
      c.status === "ready" &&
      c.id !== zone.connectionId &&
      (!zone.accountId ||
        String(c.details?.account_id ?? "") === zone.accountId)
  )
}

export function ChallengeCredentialDialog({
  zone,
  open,
  onOpenChange,
}: {
  zone: Zone
  open: boolean
  onOpenChange(open: boolean): void
}) {
  const connections = useQuery(useConnectionsList)
  const attach = useMutation(useDnsZonesSetChallengeCredential)
  const invalidate = useInvalidate()
  const [connecting, setConnecting] = useState(false)
  const [error, setError] = useState("")
  const list = candidates(connections.data?.connections ?? [], zone)

  const show = (next: boolean) => {
    if (next) setError("")
    onOpenChange(next)
  }

  const attachTo = async (connection: Connection) => {
    setError("")
    try {
      await attach.mutateAsync({
        path: { zoneId: zone.id },
        body: { connectionId: connection.id, revision: zone.revision },
      })
      invalidate(useDomainsOverview)
      invalidate(useDnsZonesGet)
      invalidate(useDomainsProject)
      onOpenChange(false)
    } catch (err) {
      if (statusOf(err) === 409) {
        invalidate(useDomainsOverview)
        invalidate(useDnsZonesGet)
        setError(
          "This domain changed since you opened it. Close this and try again."
        )
        return
      }
      setError(messageOf(err, "The token wasn't attached."))
    }
  }

  return (
    <>
      <Dialog open={open && !connecting} onOpenChange={show}>
        <DialogContent className="rounded-2xl sm:max-w-[520px]">
          <DialogHeader className="gap-1">
            <DialogTitle className="text-[17px] leading-normal font-semibold tracking-[-0.015em]">
              Set up wildcard certificates
            </DialogTitle>
            <DialogDescription className="leading-normal">
              Wildcard certificates need a second Cloudflare token with DNS Edit
              on {zone.name} only. Loomplane stores it on each server and
              cluster that serves a wildcard for this zone, so keep its scope to
              this one zone.
            </DialogDescription>
          </DialogHeader>
          {connections.status === "error" ? (
            <QueryError
              error={connections.error}
              fallback="Connected accounts couldn't be loaded."
              retry={connections.refetch}
            />
          ) : !connections.data ? (
            <Skeleton className="h-9 rounded-[9px]" />
          ) : (
            <div className="flex flex-col gap-2">
              {list.length > 0 && (
                <div
                  role="group"
                  aria-label="Cloudflare connections"
                  className="flex flex-wrap gap-1.5"
                >
                  {list.map((c) => (
                    <Button
                      key={c.id}
                      variant="outline"
                      size="sm"
                      disabled={attach.isPending}
                      onClick={() => void attachTo(c)}
                    >
                      {c.name}
                      <span className="font-mono text-[11px] text-muted-foreground">
                        {c.account}
                      </span>
                    </Button>
                  ))}
                </div>
              )}
              <Button
                variant="outline"
                size="sm"
                className="self-start"
                disabled={attach.isPending}
                onClick={() => setConnecting(true)}
              >
                Connect a Cloudflare token
              </Button>
            </div>
          )}
          {error && (
            <p role="alert" className="text-[12.5px] text-destructive">
              {error}
            </p>
          )}
        </DialogContent>
      </Dialog>
      <ConnectDialog
        open={open && connecting}
        onOpenChange={(next) => {
          if (!next) setConnecting(false)
        }}
        cloud="cloudflare"
        onConnected={(c) => {
          setConnecting(false)
          if (c.status === "ready") void attachTo(c)
        }}
      />
    </>
  )
}
