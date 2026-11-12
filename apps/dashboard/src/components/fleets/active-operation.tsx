import { ActivityIcon } from "lucide-react"
import { useCallback, useRef, useState } from "react"
import { statusOf } from "@/client/errors"
import {
  useFleetInstancesActiveOperation,
  useFleetOperationsCancel,
} from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { useFleetRefresh } from "@/components/fleets/use-fleet-refresh"
import { Button } from "@/components/ui/button"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { useOrganization } from "@/lib/organization"

type InstanceOperationScope = {
  id: string
  revision: number
  desiredGeneration: number
  status: string
}

type Feedback = { message: string; accepted: boolean; failed: boolean }

function cancellationError(error: unknown): string {
  switch (statusOf(error)) {
    case 409:
      return "The instance or operation changed. Reload both before requesting cancellation again."
    case 401:
    case 403:
      return "You do not have permission to cancel this operation."
    default:
      return "Could not confirm cancellation. Reload the current state before deciding whether to try again."
  }
}

// The parent validates the instance scope before mounting this view.
export function ActiveOperation({
  projectID,
  instance,
  onChanged,
}: {
  projectID: string
  instance: InstanceOperationScope
  onChanged: () => Promise<unknown>
}) {
  const { can } = useOrganization()
  const active = useQuery(useFleetInstancesActiveOperation, {
    path: { project_id: projectID, id: instance.id },
  })
  const cancel = useMutation(useFleetOperationsCancel)
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const [feedback, setFeedback] = useState<Feedback | null>(null)
  const { refetch, isFetching } = active
  const status = statusOf(active.error)
  const readDenied =
    active.status === "error" && (status === 401 || status === 403)
  const poll = useCallback(async () => {
    if (readDenied || isFetching || inFlight.current) return
    await refetch()
  }, [readDenied, isFetching, refetch])
  useFleetRefresh(!readDenied && !isFetching && !busy, poll)

  const operation = active.status === "error" ? undefined : active.data
  const scoped = operation?.instanceId === instance.id
  const mayRequest =
    !!operation &&
    scoped &&
    can("deployment:create") &&
    operation.kind === "create" &&
    operation.status === "waiting_approval" &&
    instance.status === "waiting_approval" &&
    operation.generation === instance.desiredGeneration &&
    instance.desiredGeneration > 0 &&
    Number.isSafeInteger(instance.revision) &&
    instance.revision > 0 &&
    !feedback
  const pending = busy || cancel.isPending || isFetching

  async function refreshBoth(): Promise<boolean> {
    const [instanceRead, operationRead] = await Promise.allSettled([
      onChanged(),
      refetch(),
    ])
    return (
      instanceRead.status === "fulfilled" &&
      (operationRead.status === "fulfilled" ||
        statusOf(operationRead.reason) === 404)
    )
  }

  async function reload() {
    if (inFlight.current) return
    inFlight.current = true
    setBusy(true)
    try {
      const refreshed = await refreshBoth()
      if (refreshed) setFeedback(null)
      else
        setFeedback({
          accepted: feedback?.accepted ?? false,
          failed: true,
          message: feedback?.accepted
            ? "Cancellation request accepted, but the current state could not be refreshed."
            : "Could not refresh the current state. Try reloading again.",
        })
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }

  async function requestCancellation() {
    if (!mayRequest || !operation || pending || inFlight.current) return
    const operationID = operation.id
    const expectedRevision = instance.revision
    inFlight.current = true
    setBusy(true)
    try {
      await cancel.mutateAsync({
        path: { project_id: projectID, id: operationID },
        body: { expectedInstanceRevision: expectedRevision },
      })
    } catch (error) {
      setFeedback({
        message: cancellationError(error),
        accepted: false,
        failed: true,
      })
      inFlight.current = false
      setBusy(false)
      return
    }
    try {
      const refreshed = await refreshBoth()
      setFeedback({
        accepted: true,
        failed: !refreshed,
        message: refreshed
          ? "Cancellation request accepted. Check the refreshed instance and operation state."
          : "Cancellation request accepted, but the current state could not be refreshed.",
      })
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }

  const unavailable = active.status === "error" || (!!operation && !scoped)
  return (
    <section
      aria-label="Active operation"
      className="grid min-w-0 gap-2 rounded-lg border p-3"
    >
      <h2 className="text-sm font-medium">Active operation</h2>
      {feedback && (
        <div
          className="flex flex-wrap items-center justify-between gap-2 text-sm"
          role={feedback.failed ? "alert" : "status"}
        >
          <p className="min-w-0 flex-1">{feedback.message}</p>
          <Button
            variant="outline"
            size="sm"
            disabled={pending}
            onClick={() => void reload()}
          >
            Reload current state
          </Button>
        </div>
      )}
      {unavailable ? (
        <ZeroState
          variant="illustrated"
          className="px-0 py-1"
          preview={status === 404}
          role={readDenied || status === 404 ? "status" : "alert"}
        >
          <ZeroStateHeader>
            <ZeroStateMedia variant="icon">
              <ActivityIcon />
            </ZeroStateMedia>
            <ZeroStateTitle>
              {readDenied
                ? "Operation access denied"
                : status === 404
                  ? "No active operation"
                  : "Could not load the active operation"}
            </ZeroStateTitle>
            <ZeroStateDescription>
              {readDenied
                ? "Your current permissions do not allow this operation view."
                : status === 404
                  ? "No unresolved operation was found for this instance."
                  : "Refresh to check the current operation. No cancellation was submitted by this read."}
            </ZeroStateDescription>
          </ZeroStateHeader>
          {!readDenied && (
            <ZeroStateContent>
              <Button
                variant="outline"
                size="sm"
                disabled={pending}
                onClick={() => void poll().catch(() => undefined)}
              >
                Refresh operation
              </Button>
            </ZeroStateContent>
          )}
        </ZeroState>
      ) : !operation ? (
        <p
          role="status"
          aria-busy="true"
          className="text-sm text-muted-foreground"
        >
          Loading active operation
        </p>
      ) : (
        <>
          <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
            <div className="grid min-w-0 gap-1 text-sm">
              <p className="font-medium">
                {operation.status.charAt(0).toUpperCase() +
                  operation.status.slice(1).replaceAll("_", " ")}
              </p>
              <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground [&>span]:break-all">
                <span>Operation {operation.id}</span>
                <span>{operation.kind}</span>
                <span>Generation {operation.generation}</span>
                <time dateTime={operation.createdAt}>
                  {new Date(operation.createdAt).toLocaleString()}
                </time>
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={pending}
                onClick={() => void poll().catch(() => undefined)}
              >
                Refresh operation
              </Button>
              {mayRequest && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={pending}
                  onClick={() => void requestCancellation()}
                >
                  Request cancellation
                </Button>
              )}
            </div>
          </div>
          {mayRequest ? (
            <p className="text-xs text-muted-foreground">
              The server rechecks admission before cancelling. An admitted
              request may require its recovery workflow.
            </p>
          ) : operation.generation !== instance.desiredGeneration ? (
            <p className="text-xs text-muted-foreground">
              This operation belongs to another generation. Review its recovery
              status before taking action.
            </p>
          ) : !can("deployment:create") ? (
            <p className="text-xs text-muted-foreground">
              Requesting cancellation requires deployment permission.
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">
              Cancellation requests are available only for a queued create
              awaiting approval.
            </p>
          )}
        </>
      )}
    </section>
  )
}
