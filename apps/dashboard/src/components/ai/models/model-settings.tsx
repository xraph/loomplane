// Changing where a model runs, and removing it. Both edit the manifest and
// plan the environment; nothing changes until the plan is approved. A model
// that runs in several environments moves and goes one environment at a
// time: the others keep their copies.
import { useState } from "react"

import {
  useAiModelDeploymentsGet,
  useAiModelDeploymentsList,
  useAiModelDeploymentsRemove,
} from "@/client/forge/loomplane/hooks"
import type { DeployResult } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Note, said } from "@/components/ai/models/deploy-parts"
import { MoveModelDialog } from "@/components/ai/models/move-model-dialog"
import {
  costsMoney,
  noEdit,
  type ModelProps,
} from "@/components/ai/models/model-overview"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { listOf } from "@/lib/models"
import { useOnce } from "@/lib/use-once"

export function ModelSettings(props: ModelProps) {
  const { status, production } = props
  const why = status.canEdit ? undefined : noEdit(status, production)
  // A model waiting for its GPU server can't move or go until that's
  // cancelled: the server says so too.
  const waiting =
    status.state === "waiting_for_server" || status.state === "needs_attention"
      ? "It's waiting for its GPU server. Cancel that on the Overview tab first."
      : undefined
  return (
    <div className="grid min-w-0 gap-5">
      <Move {...props} blocked={why ?? waiting} />
      <Remove {...props} blocked={why ?? waiting} />
    </div>
  )
}

function Panel({
  title,
  description,
  children,
  label,
}: {
  title: string
  description: string
  children: React.ReactNode
  label: string
}) {
  return (
    <section
      aria-label={label}
      className="grid min-w-0 gap-3 rounded-xl p-4 ring-1 ring-border"
    >
      <div className="grid gap-1">
        <h3 className="text-[13.5px] font-semibold">{title}</h3>
        <p className="max-w-[72ch] text-[13px] text-pretty text-muted-foreground">
          {description}
        </p>
      </div>
      {children}
    </section>
  )
}

function Move({
  status,
  projectId,
  environmentId,
  notify,
  blocked,
}: ModelProps & { blocked?: string }) {
  const [open, setOpen] = useState(false)
  const invalidate = useInvalidate()
  const here = status.environments[0]?.environment || "this environment"
  const others = status.environments.slice(1).map((e) => e.environment)
  return (
    <Panel
      label="Move"
      title="Move"
      description={`Run ${status.resource} on another GPU, server or cluster, or with another quantization or context. It keeps its name, so linked services keep their MODEL_URL, and it keeps running where it is until the move is deployed.${
        others.length > 0
          ? ` Moves ${here} only. ${listOf(others)} ${others.length > 1 ? "keep their" : "keeps its"} own copy as it is.`
          : ""
      }`}
    >
      <Button
        variant="outline"
        className="w-fit"
        disabled={!!blocked}
        onClick={() => setOpen(true)}
      >
        Move {status.resource}
      </Button>
      {blocked && (
        <p className="text-[12.5px] text-muted-foreground">{blocked}</p>
      )}
      {open && (
        <MoveModelDialog
          status={status}
          projectId={projectId}
          environmentId={environmentId}
          onClose={() => setOpen(false)}
          onMoved={(res) => {
            setOpen(false)
            invalidate(useAiModelDeploymentsGet)
            invalidate(useAiModelDeploymentsList)
            notify(movedNotice(status.resource, res))
          }}
        />
      )}
    </Panel>
  )
}

/**
 * movedNotice says what a move did. The server's message, when there is
 * one, is that a GPU server Loomplane created for the model keeps billing,
 * so it's the notice, in the money tone.
 */
function movedNotice(resource: string, res: DeployResult) {
  const message = res.message?.trim()
  const waiting = res.deployment?.phase === "waiting_for_server"
  const what = res.planId
    ? `The move of ${resource} is planned. It keeps running where it is until you approve the plan.`
    : waiting
      ? res.deployment?.message ||
        `The move of ${resource} waits for its new GPU server. It keeps running where it is meanwhile.`
      : `${resource} is moving.`
  if (message)
    return {
      title: what,
      text: message,
      tone: costsMoney(message) ? ("weld" as const) : undefined,
      planId: res.planId,
      infrastructure: costsMoney(message),
    }
  return { text: what, planId: res.planId }
}

function Remove({
  status,
  projectId,
  environmentId,
  notify,
  blocked,
}: ModelProps & { blocked?: string }) {
  const invalidate = useInvalidate()
  const remove = useMutation(useAiModelDeploymentsRemove)
  const [confirming, setConfirming] = useState(false)
  const [refused, setRefused] = useState("")
  const server = status.connectionName || "its server"
  const here = status.environments[0]?.environment || "this environment"
  // The environments that keep their own copy when this one goes.
  const others = listOf(status.environments.slice(1).map((e) => e.environment))
  // Each service in this environment that links to the model loses
  // MODEL_URL once the plan is approved. With no other copy, the server
  // refuses instead and says to unlink it first.
  const losing = others
    ? status.linked.filter((l) => l.environment === here)
    : []

  const once = useOnce()
  const go = () => once("remove", goNow)

  async function goNow() {
    setConfirming(false)
    setRefused("")
    notify(undefined)
    try {
      const res = (await remove.mutateAsync({
        path: { projectId, environmentId, resource: status.resource },
      })) as DeployResult
      const message = res.message?.trim()
      // The model is gone from the manifest, so its page is about to say
      // so; the list is what changed.
      invalidate(useAiModelDeploymentsList)
      notify({
        title: `Removing ${status.resource}`,
        text:
          message ||
          (others
            ? `The plan removes ${status.resource} from ${here}. It keeps running in ${others}. Its downloaded weights stay on ${server} as retained storage.`
            : `The plan removes ${status.resource}. Its downloaded weights stay on ${server} as retained storage.`),
        tone: message && costsMoney(message) ? "weld" : undefined,
        planId: res.planId,
        infrastructure: !!message && costsMoney(message),
      })
    } catch (err) {
      // Such as "checkout in staging links to it. Unlink it first."
      setRefused(said(err, `${status.resource} wasn't removed.`))
    }
  }

  return (
    <Panel
      label="Remove"
      title="Remove"
      description={
        others
          ? `Take ${here}'s copy of ${status.resource} out of the manifest and plan ${here}. It keeps running in ${others}.`
          : `Take ${status.resource} out of the manifest and plan its environment. A service that links to it has to be unlinked first.`
      }
    >
      <Button
        variant="outline"
        className="w-fit text-destructive hover:text-destructive"
        disabled={!!blocked || remove.isPending}
        onClick={() => setConfirming(true)}
      >
        {remove.isPending && <Spinner />}
        Remove {status.resource}
      </Button>
      {blocked && (
        <p className="text-[12.5px] text-muted-foreground">{blocked}</p>
      )}
      {refused && (
        <p role="alert" className="text-[13px] text-pretty text-destructive">
          {refused}
        </p>
      )}
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {status.resource}?</AlertDialogTitle>
            <AlertDialogDescription>
              {others
                ? `Removes ${status.resource} from ${here}. It keeps running in ${others}.`
                : "The model stops."}{" "}
              The downloaded weights stay on {server} as retained storage.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {losing.length > 0 && (
            <div
              aria-label="Linked services"
              role="group"
              className="grid gap-1.5"
            >
              {losing.map((l) => (
                <Note key={l.service} tone="weld">
                  {l.service} gets no MODEL_URL in {here} once the plan is
                  approved. The plan warns need-off for it.
                </Note>
              ))}
            </div>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <Button variant="destructive" onClick={() => void go()}>
              Remove {status.resource}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Panel>
  )
}
