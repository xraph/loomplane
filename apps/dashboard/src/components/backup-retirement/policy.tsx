import { useRef, useState } from "react"
import { useMutation, useQuery } from "@/client/hooks"
import {
  useRetentionPoliciesGet,
  useRetentionPoliciesSet,
} from "@/client/retention-policies"
import type { RetentionInput } from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useOrganization } from "@/lib/organization"
import { useDeploymentWrites } from "@/lib/deployment-writes"
import { useRecoveryPolling } from "@/lib/recovery-polling"
import { QueryError } from "@/components/query-error"
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
  AlertDialogAction,
} from "@/components/ui/alert-dialog"
import { RetentionPolicyChanges } from "./policy-history"

type Props = {
  project: string
  anchor: string
  settings: RetentionInput
  enableBlocked: string | null
  onChanged: () => Promise<unknown>
  onOpenRetirement: (id: string) => void
}
type Confirmation = {
  anchor: string
  policyId?: string
  revision: number
  enabled: boolean
  policy: RetentionInput
}

export function RetentionPolicyControls({
  project,
  anchor,
  settings,
  enableBlocked,
  onChanged,
  onOpenRetirement,
}: Props) {
  const { can } = useOrganization()
  const owner = can("service_backup:retire")
  const { busy, onPendingChange } = useDeploymentWrites(project)
  const state = useQuery(
    useRetentionPoliciesGet,
    { path: { id: anchor } },
    { staleTime: 0 }
  )
  const save = useMutation(useRetentionPoliciesSet)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [notice, setNotice] = useState("")
  const [needsInspection, setNeedsInspection] = useState(false)
  const [history, setHistory] = useState(false)
  const focusTarget = useRef<HTMLElement>(null)
  const { refetch } = state
  useRecoveryPolling(false, refetch)
  const policy = state.data?.policy
  const loaded =
    state.status === "success" &&
    typeof state.data?.configured === "boolean" &&
    (!state.data.configured ||
      (!!policy && Number.isInteger(policy.revision) && policy.revision > 0))
  const changed =
    !!confirmation &&
    (confirmation.anchor !== anchor ||
      confirmation.revision !== (policy?.revision ?? 0) ||
      confirmation.policyId !== policy?.id)
  const matching =
    policy?.policy.olderThanDays === settings.olderThanDays &&
    policy?.policy.keepLast === settings.keepLast
  function confirm(enabled: boolean) {
    if (
      !loaded ||
      !owner ||
      busy ||
      needsInspection ||
      (enabled && enableBlocked)
    )
      return
    setNotice("")
    setConfirmation({
      anchor,
      policyId: policy?.id,
      revision: policy?.revision ?? 0,
      enabled,
      policy: { ...(enabled ? settings : policy!.policy) },
    })
  }
  async function inspect() {
    try {
      await refetch()
      setConfirmation(null)
      setNeedsInspection(false)
      setNotice(
        "Current policy loaded. Review its settings before making another change."
      )
    } catch (error) {
      setNotice(
        messageOf(
          error,
          "Current policy could not be inspected. Try again before making another change."
        )
      )
    }
  }
  async function submit() {
    if (
      !confirmation ||
      !loaded ||
      changed ||
      !owner ||
      busy ||
      needsInspection ||
      (confirmation.enabled && enableBlocked)
    )
      return
    onPendingChange(true)
    let accepted = false
    try {
      await save.mutateAsync({
        path: { id: confirmation.anchor },
        body: {
          revision: confirmation.revision,
          enabled: confirmation.enabled,
          policy: confirmation.policy,
        },
      })
      accepted = true
      setConfirmation(null)
      await refetch()
      await onChanged()
      setNotice("")
    } catch (error) {
      setNeedsInspection(true)
      setNotice(
        accepted
          ? "Policy saved, but the refreshed state could not be loaded. Inspect the current policy before making another change."
          : messageOf(
              error,
              "The policy change was not confirmed. Inspect the current policy before making another change."
            )
      )
    } finally {
      onPendingChange(false)
    }
  }
  const inspection = needsInspection && (
    <Button
      type="button"
      size="sm"
      variant="outline"
      disabled={busy || state.isFetching}
      onClick={() => void inspect()}
    >
      Inspect current policy
    </Button>
  )
  return (
    <section
      ref={focusTarget}
      tabIndex={-1}
      className="grid min-w-0 gap-2 rounded-xl bg-card p-3 ring-1 ring-border outline-none focus-visible:ring-2 focus-visible:ring-ring"
      aria-label="Automatic retention policy"
    >
      {state.status === "pending" ? (
        <p role="status" className="text-[13px]">
          Loading automatic policy…
        </p>
      ) : state.status === "error" ? (
        <QueryError
          error={state.error}
          fallback="Could not inspect automatic policy"
          retry={refetch}
          label="Retry policy inspection"
        />
      ) : !loaded ? (
        <Alert variant="destructive">
          <AlertDescription>
            Policy response is incomplete. Reload before making a change.
          </AlertDescription>
        </Alert>
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="min-w-0">
              <p className="text-[13px] font-medium">
                {policy?.enabled
                  ? "Automatic deletion enabled"
                  : policy
                    ? "Automatic deletion paused"
                    : "No automatic deletion policy"}
              </p>
              {policy && (
                <p className="text-xs text-muted-foreground">
                  Older than {policy.policy.olderThanDays} days · Keep{" "}
                  {policy.policy.keepLast} newest · Revision {policy.revision}
                </p>
              )}
            </div>
            <div className="flex flex-wrap gap-2">
              {owner && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={
                    busy ||
                    state.isFetching ||
                    !!enableBlocked ||
                    needsInspection ||
                    (!!policy?.enabled && matching)
                  }
                  onClick={() => confirm(true)}
                >
                  {policy?.enabled
                    ? "Update automatic policy"
                    : "Enable automatic deletion"}
                </Button>
              )}
              {owner && policy?.enabled && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={busy || state.isFetching || needsInspection}
                  onClick={() => confirm(false)}
                >
                  Pause automatic deletion
                </Button>
              )}
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={busy}
                aria-expanded={history}
                onClick={() => setHistory(!history)}
              >
                Policy changes
              </Button>
              {policy?.lastRetirementId && (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => onOpenRetirement(policy.lastRetirementId!)}
                >
                  Last deletion
                </Button>
              )}
            </div>
          </div>
          {owner && enableBlocked && (
            <p className="text-xs text-muted-foreground">{enableBlocked}</p>
          )}
          {policy?.lastError && (
            <Alert variant="destructive">
              <AlertDescription>{policy.lastError}</AlertDescription>
            </Alert>
          )}
        </>
      )}
      {!confirmation && notice && (
        <Alert variant={needsInspection ? "destructive" : "default"}>
          <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
            <span>{notice}</span>
            {inspection}
          </AlertDescription>
        </Alert>
      )}
      {history && <RetentionPolicyChanges key={anchor} anchor={anchor} />}
      <AlertDialog
        open={!!confirmation}
        onOpenChange={(value) => {
          if (!value && !busy) setConfirmation(null)
        }}
      >
        <AlertDialogContent finalFocus={focusTarget}>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirmation?.enabled
                ? "Enable automatic backup deletion?"
                : "Pause automatic backup deletion?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirmation?.enabled
                ? `Eligible backup files older than ${confirmation.policy.olderThanDays} days may be permanently deleted, keeping at least ${confirmation.policy.keepLast} newest points and a restore-tested point. Retained bytes and current owner permission are checked before deletion. Backup scheduling stays separate.`
                : "Future automatic selections will stop. Deletions already accepted may still finish. The saved age and count settings stay in place."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {changed && !busy && (
            <Alert variant="destructive">
              <AlertDescription>
                The policy changed while this confirmation was open. Close it
                and inspect the current settings.
              </AlertDescription>
            </Alert>
          )}
          {notice && (
            <Alert variant="destructive">
              <AlertDescription>{notice}</AlertDescription>
            </Alert>
          )}
          {inspection}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant={confirmation?.enabled ? "destructive" : "outline"}
              disabled={
                busy ||
                !owner ||
                !loaded ||
                changed ||
                needsInspection ||
                (!!confirmation?.enabled && !!enableBlocked)
              }
              onClick={(event) => {
                event.preventDefault()
                void submit()
              }}
            >
              {busy
                ? "Saving policy…"
                : confirmation?.enabled
                  ? "Enable automatic deletion"
                  : "Pause automatic deletion"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
