import { useState } from "react"
import { useQuery } from "@/client/hooks"
import { useRetentionPoliciesHistory } from "@/client/retention-policies"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { formatDate } from "@/lib/format"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { RetirementEmpty } from "./shared"

export function RetentionPolicyChanges({ anchor }: { anchor: string }) {
  const [before, setBefore] = useState("")
  const [newer, setNewer] = useState<string[]>([])
  const history = useQuery(
    useRetentionPoliciesHistory,
    {
      path: { id: anchor },
      query: { limit: 10, before: before || undefined },
    },
    { staleTime: 0 }
  )
  useRecoveryPolling(false, history.refetch)
  if (history.status === "pending")
    return <p role="status">Loading policy changes…</p>
  if (history.status === "error")
    return (
      <QueryError
        error={history.error}
        fallback="Could not load policy changes"
        retry={history.refetch}
      />
    )
  if (!history.data?.events.length)
    return (
      <RetirementEmpty
        title="No policy changes yet"
        description="An owner can enable automatic deletion after reviewing the recovery history."
      />
    )
  return (
    <div className="grid min-w-0 gap-2" aria-label="Policy changes">
      {history.data.events.map((event) => (
        <div
          key={event.revision}
          className="flex min-w-0 flex-wrap justify-between gap-x-4 gap-y-1 border-t py-2 text-xs"
        >
          <div>
            <span className="font-medium">Revision {event.revision}</span> ·{" "}
            {event.enabled ? "Enabled" : "Paused"}
            <p className="text-muted-foreground">
              Older than {event.policy.olderThanDays} days · Keep{" "}
              {event.policy.keepLast} newest
            </p>
          </div>
          <div className="min-w-0 text-muted-foreground">
            <p>{formatDate(event.createdAt)}</p>
            <p className="break-all">Owner {event.authorizedBy}</p>
          </div>
        </div>
      ))}
      {(newer.length > 0 || history.data.nextBefore) && (
        <div className="flex justify-between gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={!newer.length || history.isFetching}
            onClick={() => {
              setBefore(newer.at(-1) ?? "")
              setNewer(newer.slice(0, -1))
            }}
          >
            Newer changes
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!history.data?.nextBefore || history.isFetching}
            onClick={() => {
              setNewer([...newer, before])
              setBefore(history.data?.nextBefore ?? "")
            }}
          >
            Older changes
          </Button>
        </div>
      )}
    </div>
  )
}
