import { useState } from "react"
import { useMutation, useQuery } from "@/client/hooks"
import {
  useNativeRetentionPoliciesGet,
  useNativeRetentionPoliciesSet,
  useNativeRetentionPoliciesHistory,
} from "@/client/native-retention-policies"
import { messageOf } from "@/client/errors"
import { useOrganization } from "@/lib/organization"
import { useDeploymentWrites } from "@/lib/deployment-writes"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { formatDate } from "@/lib/format"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog"
import { NativeEmpty } from "@/components/native-recovery-empty"
import { QueryError } from "@/components/query-error"

export type NativePolicyProps = {
  source: string
  project: string
  settings: { olderThanDays: number; keepLast: number }
  enableBlocked: string | null
  onDetails: (id: string) => void
}
type Proposal = {
  revision: number
  enabled: boolean
  policy: NativePolicyProps["settings"]
}
export function NativeRetentionPolicyControls(props: NativePolicyProps) {
  const { session } = useOrganization()
  return (
    <NativePolicyContent
      key={`${session?.orgId ?? ""}:${props.source}`}
      {...props}
    />
  )
}
function NativePolicyContent({
  source,
  project,
  settings,
  enableBlocked,
  onDetails,
}: NativePolicyProps) {
  const { can } = useOrganization()
  const state = useQuery(
    useNativeRetentionPoliciesGet,
    { path: { id: source } },
    { staleTime: 0 }
  )
  const save = useMutation(useNativeRetentionPoliciesSet)
  const { busy, onPendingChange } = useDeploymentWrites(project)
  const [proposal, setProposal] = useState<Proposal | null>(null)
  const [error, setError] = useState("")
  const [inspectRequired, setInspectRequired] = useState(false)
  const [history, setHistory] = useState(false)
  const policy = state.data?.policy
  const owner = can("service_backup:retire")
  const changed =
    !!proposal &&
    ((policy?.revision ?? 0) !== proposal.revision ||
      (proposal.enabled &&
        (settings.olderThanDays !== proposal.policy.olderThanDays ||
          settings.keepLast !== proposal.policy.keepLast ||
          !!enableBlocked)))
  useRecoveryPolling(false, state.refetch)
  async function inspect() {
    try {
      await state.refetch()
      setInspectRequired(false)
      setError("")
    } catch (cause) {
      setError(messageOf(cause, "Could not inspect the current native policy"))
    }
  }
  async function submit() {
    if (
      !proposal ||
      busy ||
      !owner ||
      changed ||
      state.status !== "success" ||
      inspectRequired
    )
      return
    onPendingChange(true)
    setError("")
    try {
      await save.mutateAsync({ path: { id: source }, body: proposal })
      setProposal(null)
      await Promise.allSettled([state.refetch()])
    } catch (cause) {
      setProposal(null)
      setInspectRequired(true)
      setError(
        messageOf(
          cause,
          "The save was not confirmed. Inspect the current policy before retrying."
        )
      )
    } finally {
      onPendingChange(false)
    }
  }
  return (
    <section
      aria-label="Automatic native retention"
      className="grid min-w-0 gap-2 rounded-md border p-3 text-xs"
    >
      {state.status === "error" && (
        <QueryError
          error={state.error}
          fallback="Could not inspect native retention policy"
          retry={state.refetch}
        />
      )}
      {state.status === "pending" && !state.data ? (
        <p role="status">Loading native retention policy…</p>
      ) : (
        state.data && (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p>
                Automatic native deletion is {policy?.enabled ? "on" : "off"}
                {policy ? ` · revision ${policy.revision}` : ""}
              </p>
              <div className="flex flex-wrap gap-2">
                {owner && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={
                      busy ||
                      state.status !== "success" ||
                      !!enableBlocked ||
                      inspectRequired
                    }
                    onClick={() =>
                      setProposal({
                        revision: policy?.revision ?? 0,
                        enabled: true,
                        policy: { ...settings },
                      })
                    }
                  >
                    {policy?.enabled
                      ? "Update native retention policy"
                      : "Enable automatic native deletion"}
                  </Button>
                )}
                {owner && policy?.enabled && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={
                      busy || state.status !== "success" || inspectRequired
                    }
                    onClick={() =>
                      setProposal({
                        revision: policy.revision,
                        enabled: false,
                        policy: { ...policy.policy },
                      })
                    }
                  >
                    Pause native retention
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setHistory((value) => !value)}
                >
                  Native policy changes
                </Button>
              </div>
            </div>
            {policy && (
              <p className="text-muted-foreground">
                Older than {policy.policy.olderThanDays} days · keep{" "}
                {policy.policy.keepLast} newest independent backups · authorized
                by {policy.authorizedBy}
              </p>
            )}
            {owner && enableBlocked && (
              <p className="text-muted-foreground">{enableBlocked}</p>
            )}
            {policy?.lastRetirementId && (
              <Button
                size="sm"
                variant="ghost"
                className="justify-self-start"
                onClick={() => onDetails(policy.lastRetirementId!)}
              >
                Last native deletion
              </Button>
            )}
            {policy?.lastError && (
              <Alert variant="destructive">
                <AlertDescription>{policy.lastError}</AlertDescription>
              </Alert>
            )}
          </>
        )
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {inspectRequired && (
        <Button
          size="sm"
          variant="outline"
          disabled={busy || state.isFetching}
          onClick={() => void inspect()}
        >
          Inspect native policy
        </Button>
      )}
      {history && <NativePolicyHistory source={source} />}
      <AlertDialog
        open={!!proposal}
        onOpenChange={(open) => {
          if (!open && !busy) setProposal(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {proposal?.enabled
                ? "Enable this native deletion rule?"
                : "Pause automatic native retention?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {proposal?.enabled
                ? `You authorize permanent deletion of eligible independent backups older than ${proposal.policy.olderThanDays} days while keeping at least ${proposal.policy.keepLast} newest points and a newer SQL-verified witness. The provider's automated recovery chain is excluded.`
                : "Future selections stop. Previously admitted deletions may still finish, and their source account references stay protected."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {changed && (
            <p role="status">
              The policy or preview changed. Close this confirmation and inspect
              it again.
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <Button
              variant={proposal?.enabled ? "destructive" : "outline"}
              disabled={
                busy ||
                !owner ||
                changed ||
                state.status !== "success" ||
                inspectRequired
              }
              onClick={() => void submit()}
            >
              Confirm native policy
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
function NativePolicyHistory({ source }: { source: string }) {
  const [pages, setPages] = useState<string[]>([""])
  const changes = useQuery(
    useNativeRetentionPoliciesHistory,
    {
      path: { id: source },
      query: { limit: 10, before: pages.at(-1) || undefined },
    },
    { staleTime: 0 }
  )
  useRecoveryPolling(false, changes.refetch)
  return (
    <div className="grid min-w-0 gap-2">
      {changes.status === "error" && (
        <QueryError
          error={changes.error}
          fallback="Could not read native policy changes"
          retry={changes.refetch}
        />
      )}
      {changes.status === "pending" && !changes.data ? (
        <p role="status">Reading native policy revisions…</p>
      ) : changes.data?.events.length ? (
        <ul aria-label="Native policy revisions" className="divide-y">
          {changes.data.events.map((event) => (
            <li
              key={event.revision}
              className="flex flex-wrap justify-between gap-2 py-1"
            >
              <span>
                Revision {event.revision} ·{" "}
                {event.enabled ? "enabled" : "paused"} ·{" "}
                {event.policy.olderThanDays} days / keep {event.policy.keepLast}
              </span>
              <span>
                {event.authorizedBy} · {formatDate(event.createdAt)}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        changes.status !== "error" && (
          <NativeEmpty title="No native policy changes">
            An owner's saved rule and authorization will appear here.
          </NativeEmpty>
        )
      )}
      {(pages.length > 1 || changes.data?.nextBefore) && (
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={pages.length === 1}
            onClick={() => setPages((value) => value.slice(0, -1))}
          >
            Newer policy changes
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!changes.data?.nextBefore || changes.status !== "success"}
            onClick={() =>
              setPages((value) => [...value, changes.data!.nextBefore!])
            }
          >
            Older policy changes
          </Button>
        </div>
      )}
    </div>
  )
}
