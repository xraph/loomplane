// What the notification inbox, its settings, workspace webhooks and the top
// bar's bell share: polling, access-loss handling and how a notice reads.
import { useEffect, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"

// Relay delivery and Herald inbox changes are read through the generated
// queries until they have bindings on the shared organization stream.
export function useNotificationRefresh(
  refetch: () => Promise<unknown>,
  every = 10_000
) {
  useEffect(() => {
    const refresh = () => {
      if (!document.hidden) void refetch().catch(() => undefined)
    }
    const timer = window.setInterval(refresh, every)
    window.addEventListener("focus", refresh)
    document.addEventListener("visibilitychange", refresh)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener("focus", refresh)
      document.removeEventListener("visibilitychange", refresh)
    }
  }, [refetch, every])
}

function authorizationFailed(error: unknown): boolean {
  return statusOf(error) === 401 || statusOf(error) === 403
}

// Keep revoked data hidden until a fresh read succeeds.
export function useAuthorizationFailure(query: {
  status: string
  error: unknown
}) {
  const [denied, setDenied] = useState(false)
  const nextDenied =
    query.status === "success"
      ? false
      : denied || authorizationFailed(query.error)
  if (denied !== nextDenied) setDenied(nextDenied)
  return nextDenied
}

// A deployment's result, in the status colors used everywhere else.
export const noticeTone: Record<string, string> = {
  succeeded: "bg-overdye",
  failed: "bg-failed",
  blocked: "bg-failed",
  waiting: "bg-weld",
  canceled: "bg-muted-foreground/50",
}

export function safeAction(value: string): boolean {
  if (!value) return false
  try {
    const url = new URL(value, window.location.origin)
    return (
      (url.protocol === "https:" || url.protocol === "http:") &&
      !url.username &&
      !url.password
    )
  } catch {
    return false
  }
}

export function emailStatus(status: string) {
  const labels: Record<string, string> = {
    pending: "Email pending",
    sent: "Email sent",
    failed: "Email failed",
    skipped: "Email skipped",
  }
  return labels[status]
}

export function ReadError({
  error,
  fallback,
  retry,
  label,
}: {
  error: unknown
  fallback: string
  retry: () => void
  label: string
}) {
  return (
    <Alert
      variant="destructive"
      className="flex flex-wrap items-center justify-between gap-3"
    >
      <AlertDescription>{messageOf(error, fallback)}</AlertDescription>
      <Button variant="outline" size="sm" onClick={retry}>
        {label}
      </Button>
    </Alert>
  )
}
