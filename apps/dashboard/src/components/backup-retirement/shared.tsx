import { useRef, useState, type ReactNode } from "react"
import { DatabaseIcon } from "lucide-react"
import type { Retirement } from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useDeploymentWrites } from "@/lib/deployment-writes"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
} from "@/components/zero-state"

export function RetirementEmpty({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children?: ReactNode
}) {
  return (
    <ZeroState className="border">
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <DatabaseIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      {children && <ZeroStateContent>{children}</ZeroStateContent>}
    </ZeroState>
  )
}

export function useDeletionRequest(
  project: string,
  onChanged: () => Promise<unknown>
) {
  const { busy, onPendingChange } = useDeploymentWrites(project)
  const keys = useRef<Record<string, string>>({})
  const [error, setError] = useState("")
  async function run(
    key: string,
    submit: (requestId: string) => Promise<Retirement>
  ) {
    if (busy) return
    onPendingChange(true)
    setError("")
    const requestId = (keys.current[key] ??= crypto.randomUUID())
    try {
      const operation = await submit(requestId)
      delete keys.current[key]
      await Promise.allSettled([onChanged()])
      return operation
    } catch (cause) {
      setError(
        messageOf(
          cause,
          "The request was not confirmed. You can retry this request."
        )
      )
    } finally {
      onPendingChange(false)
    }
  }
  return { busy, error, run }
}

export function deletionStatus(status: string) {
  return (
    (
      {
        queued: "Deletion queued",
        running: "Deleting backup",
        failed: "Deletion needs attention",
        succeeded: "Backup file deleted",
      } as Record<string, string>
    )[status] ?? "Restore blocked"
  )
}

export function retentionDecision(reason: string) {
  return (
    (
      {
        eligible: "Meets age and count settings",
        retirement_active: "An earlier deletion must finish first",
        newest: "Kept by the minimum count",
        last_verified: "Latest restore-tested recovery point",
        active_restore: "A recovery copy still uses this backup",
        inventory_incomplete: "The history is incomplete",
        no_verified_backup: "No restore-tested recovery point",
        recent: "Newer than the age limit",
        not_complete: "Not available for deletion",
      } as Record<string, string>
    )[reason] ?? "Needs review"
  )
}

export function deletionMessage(message: string) {
  if (message.startsWith("Retirement did not finish."))
    return "The backup deletion did not finish. This point remains unavailable for new restores. Check the retained recovery point and retry."
  if (
    message.startsWith("Retirement worker stopped.") ||
    message.startsWith("Worker lease expired")
  )
    return "The worker stopped before recording a result. This point remains unavailable for new restores. Check the retained recovery point before retrying."
  return message
}
