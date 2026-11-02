// A deployment that reached a capacity limit pauses rather than fails. This
// card says why, one line per block, and lets someone who can approve
// deployments resume it (which runs the same capacity checks again) or
// cancel it. It reads nothing about the Cloud itself: a project member may
// not be allowed to see the Cloud's capacity, so the reasons come from the
// deployment's own wait.
import { PauseIcon } from "lucide-react"
import { useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useDeploymentsCancelCapacity,
  useDeploymentsForPlan,
  useDeploymentsGet,
  useDeploymentsResumeCapacity,
} from "@/client/forge/loomplane/hooks"
import type { CapacityBlock, Deployment } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import { type Tone, toneClass } from "@/components/runs"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { blockText, staleText } from "@/lib/capacity"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

// Blocks someone has to act on read as failed; the rest are on their way. A
// provider failure waits too: Loomplane retries it on its own.
function blockTone(b: CapacityBlock): Tone {
  switch (b.reason) {
    case "provisioning":
      return "flight"
    case "expansion_approval":
    case "stale_inventory":
    case "provider_failure":
      return "waiting"
    default:
      return "failed"
  }
}

export function DeploymentCapacityWait({
  deployment,
}: {
  deployment: Deployment
}) {
  const wait = deployment.capacityWait
  // The realtime stream sends capacity_wait: null where REST leaves it out.
  if (deployment.status !== "paused" || !wait) return null
  // Not keyed by revision: after a 409 the reload sentence stays while the
  // card shows the new wait, until the next Resume or Cancel clears it.
  return <WaitCard deployment={deployment} />
}

function WaitCard({ deployment: d }: { deployment: Deployment }) {
  const wait = d.capacityWait!
  const blocks = wait.blocks ?? []
  const { can } = useOrganization()
  const invalidate = useInvalidate()
  const resume = useMutation(useDeploymentsResumeCapacity)
  const cancel = useMutation(useDeploymentsCancelCapacity)
  const titleId = useId()
  const [error, setError] = useState("")
  const [confirming, setConfirming] = useState(false)
  // Read the deployment again only after a stale write: until then the page
  // already follows it.
  const [stale, setStale] = useState(false)
  // Nothing reads this result on purpose. The page follows
  // useDeploymentsForPlan, which the 409 path invalidates; this GET is just a
  // fresh read of the deployment, so don't rewire the card to it.
  const reload = useQueries(
    useDeploymentsGet,
    stale ? [{ path: { id: d.id } }] : []
  )
  const busy = resume.isPending || cancel.isPending
  const approver = can("deployment:approve")
  const cloudId =
    can("organization_cloud:manage") && blocks.find((b) => b.cloudId)?.cloudId

  async function act(which: typeof resume, fallback: string): Promise<boolean> {
    setError("")
    try {
      await which.mutateAsync({
        path: { id: d.id },
        body: { expectedRevision: wait.revision },
      })
      invalidate(useDeploymentsForPlan, { path: { id: d.planId } })
      return true
    } catch (e) {
      if (statusOf(e) === 409) {
        setError(staleText)
        if (stale) reload.refetch().catch(() => undefined)
        else setStale(true)
        invalidate(useDeploymentsForPlan, { path: { id: d.planId } })
      } else {
        setError(messageOf(e, fallback))
      }
      return false
    }
  }

  return (
    <section
      aria-labelledby={titleId}
      className="flex flex-col gap-3 rounded-xl border bg-card p-4 md:p-5"
    >
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="flex min-w-0 flex-[1_1_320px] items-start gap-3">
          <span
            className={cn(
              "flex size-9 shrink-0 items-center justify-center rounded-[10px]",
              toneClass.waiting.tint,
              toneClass.waiting.ink
            )}
          >
            <PauseIcon aria-hidden="true" className="size-[18px]" />
          </span>
          <div className="grid min-w-0 gap-1">
            <h2 id={titleId} className="text-base font-semibold tracking-tight">
              Paused for capacity
            </h2>
            {approver && (
              <p className="text-[13px] text-muted-foreground">
                Resume runs the same capacity checks again. If there still
                isn&apos;t room, the deployment stays paused.
              </p>
            )}
          </div>
        </div>
        {(approver || cloudId) && (
          <div className="flex flex-wrap gap-2">
            {cloudId && (
              <Button
                variant="outline"
                nativeButton={false}
                render={
                  <Link
                    to="/clouds/$id"
                    params={{ id: cloudId }}
                    search={{ project: d.projectId, tab: "capacity" }}
                  />
                }
              >
                Review capacity
              </Button>
            )}
            {approver && (
              <>
                <Button
                  variant="outline"
                  className="text-destructive"
                  disabled={busy}
                  onClick={() => setConfirming(true)}
                >
                  Cancel deployment
                </Button>
                <Button
                  disabled={busy}
                  onClick={() =>
                    void act(resume, "The deployment couldn't be resumed.")
                  }
                >
                  {resume.isPending && <Spinner />}
                  Resume
                </Button>
              </>
            )}
          </div>
        )}
      </div>
      {blocks.length > 0 && (
        <ul className="grid gap-2 border-t pt-3 text-[13px]">
          {blocks.map((b, i) => (
            <li
              key={`${b.reason}:${b.resource}:${b.targetId}:${i}`}
              className="flex min-w-0 items-start gap-2.5"
            >
              <span
                aria-hidden="true"
                className={cn(
                  "mt-1.5 size-1.5 shrink-0 rounded-full",
                  toneClass[blockTone(b)].dot
                )}
              />
              <span className="min-w-0 break-words">{blockText(b)}</span>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Cancel this deployment?</AlertDialogTitle>
            <AlertDialogDescription>
              It stops waiting for capacity and nothing more is deployed. Steps
              that already finished stay as they are.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep waiting</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={busy}
              onClick={() =>
                void act(cancel, "The deployment couldn't be canceled.").then(
                  () => setConfirming(false)
                )
              }
            >
              {cancel.isPending && <Spinner />}
              Cancel the deployment
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
