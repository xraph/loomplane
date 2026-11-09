// Cancelling a running deployment. The action that's running finishes, so a
// provider is never left halfway through a change, and the deployment stops
// before its next one. That needs a step that hasn't started. Steps that
// finished stay as they are.
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useDeploymentsCancel,
  useDeploymentsForPlan,
  useDeploymentsList,
} from "@/client/forge/loomplane/hooks"
import type { Deployment } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useOrganization } from "@/lib/organization"

export function CancelDeployment({
  deployment: d,
}: {
  deployment: Deployment
}) {
  const { can } = useOrganization()
  const cancel = useMutation(useDeploymentsCancel)
  const invalidate = useInvalidate()
  const [confirming, setConfirming] = useState(false)
  if (d.status !== "running" || !can("deployment:approve")) return null
  if (d.cancelRequestedAt) {
    return (
      <Button variant="outline" disabled>
        <Spinner />
        Canceling after this step
      </Button>
    )
  }
  // A pending step with a start time belongs to an action under way.
  if (!d.steps.some((s) => s.status === "pending" && !s.startedAt)) return null

  const run = async () => {
    try {
      const out = await cancel.mutateAsync({ path: { id: d.id } })
      invalidate(useDeploymentsForPlan, { path: { id: d.planId } })
      invalidate(useDeploymentsList)
      setConfirming(false)
      toast.success(
        out.status === "cancelled"
          ? "Deployment canceled"
          : "It stops once the step that's running finishes"
      )
    } catch (err) {
      toast.error(messageOf(err, "The deployment couldn't be canceled."))
    }
  }

  return (
    <>
      <Button
        variant="outline"
        className="text-destructive"
        disabled={cancel.isPending}
        onClick={() => setConfirming(true)}
      >
        Cancel
      </Button>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Cancel this deployment?</AlertDialogTitle>
            <AlertDialogDescription>
              The step that&apos;s running finishes first, then the deployment
              stops and nothing after it is deployed. Steps that already
              finished stay as they are.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep deploying</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={cancel.isPending}
              onClick={() => void run()}
            >
              {cancel.isPending && <Spinner />}
              Cancel the deployment
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
