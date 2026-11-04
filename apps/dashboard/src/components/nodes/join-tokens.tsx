import { useEffect, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useApiKeysList,
  useNodeJoinTokensCancel,
  useNodeJoinTokensList,
} from "@/client/forge/loomplane/hooks"
import type { PendingNodeJoinToken } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { usePeople, useNow } from "@/lib/use-runs"

/** minutesLeft is how long a token has, as the list shows it. */
export function minutesLeft(expiresAt: string, now: number): string {
  const m = Math.ceil((new Date(expiresAt).getTime() - now) / 60_000)
  return m <= 1 ? "Under a minute left" : `${m} min left`
}

/**
 * JoinTokens lists the join tokens no machine has used yet, so someone who
 * may make them can see what is still open and cancel one. It shows nothing
 * when there are none. nodes changes whenever the node list does: a join
 * spends a token, so the list is asked for again then.
 */
export function JoinTokens({ nodes }: { nodes: unknown }) {
  const tokens = useQuery(useNodeJoinTokensList)
  const now = useNow(30_000)
  const { refetch } = tokens
  useEffect(() => {
    void refetch()
  }, [nodes, refetch])

  const open = (tokens.data?.tokens ?? []).filter(
    (t) => new Date(t.expiresAt).getTime() > now
  )
  if (open.length === 0) return null

  return (
    <Card className="gap-0 py-0">
      <div className="px-4 pt-4 pb-2">
        <h2 className="text-[13.5px] font-semibold">Unused join tokens</h2>
        <p className="text-[12.5px] text-muted-foreground">
          Each one lets a single machine join until it expires. Cancel any you
          no longer need.
        </p>
      </div>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Token</TableHead>
            <TableHead>Made by</TableHead>
            <TableHead className="hidden md:table-cell">For</TableHead>
            <TableHead>Expires</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {open.map((t) => (
            <TokenRow key={t.id} token={t} now={now} />
          ))}
        </TableBody>
      </Table>
    </Card>
  )
}

function TokenRow({
  token: t,
  now,
}: {
  token: PendingNodeJoinToken
  now: number
}) {
  const people = usePeople()
  const keys = useQuery(useApiKeysList)
  const keyName = t.mintedByKeyId
    ? (keys.data?.keys?.find((k) => k.id === t.mintedByKeyId)?.name ??
      t.mintedByKeyId)
    : ""
  const labels = Object.entries(t.labels ?? {}).map(([k, v]) => `${k}=${v}`)
  return (
    <TableRow>
      <TableCell className="font-mono text-[12.5px]">
        lpj_live_…{t.hint}
      </TableCell>
      <TableCell className="text-[13px]">
        {people(t.mintedBy).name}
        {keyName && (
          <div className="text-xs text-muted-foreground">
            with the API key <span className="font-mono">{keyName}</span>
          </div>
        )}
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <div className="flex flex-wrap gap-1">
          {t.edge && (
            <Badge variant="outline" className="font-normal">
              {t.edgeScope === "instance" ? "Instance edge" : "Edge"}
            </Badge>
          )}
          {t.endpoints.map((e) => (
            <Badge key={e} variant="secondary" className="font-mono">
              {e}
            </Badge>
          ))}
          {labels.map((l) => (
            <span key={l} className="font-mono text-xs text-muted-foreground">
              {l}
            </span>
          ))}
          {!t.edge && t.endpoints.length === 0 && labels.length === 0 && (
            <span className="text-xs text-muted-foreground">
              Whatever the machine allows
            </span>
          )}
        </div>
      </TableCell>
      <TableCell className="text-[12.5px] text-muted-foreground">
        {minutesLeft(t.expiresAt, now)}
      </TableCell>
      <TableCell className="text-right">
        <CancelToken token={t} />
      </TableCell>
    </TableRow>
  )
}

function CancelToken({ token: t }: { token: PendingNodeJoinToken }) {
  const cancel = useMutation(useNodeJoinTokensCancel)
  const invalidate = useInvalidate()
  const [open, setOpen] = useState(false)
  const [error, setError] = useState("")

  const confirm = async () => {
    setError("")
    try {
      await cancel.mutateAsync({ path: { id: t.id } })
      invalidate(useNodeJoinTokensList)
      toast.success("Join token cancelled")
      setOpen(false)
    } catch (err) {
      setError(messageOf(err, "The token wasn't cancelled."))
    }
  }

  return (
    <AlertDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setError("")
      }}
    >
      <AlertDialogTrigger
        render={
          <Button
            variant="outline"
            size="sm"
            className="text-destructive hover:text-destructive"
          >
            Cancel
          </Button>
        }
      />
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Cancel lpj_live_…{t.hint}?</AlertDialogTitle>
          <AlertDialogDescription>
            No machine can join with it after this. A machine already running
            the command fails to join and needs a new token.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <Button
            variant="destructive"
            disabled={cancel.isPending}
            onClick={() => void confirm()}
          >
            {cancel.isPending && <Spinner />}
            Cancel token
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
