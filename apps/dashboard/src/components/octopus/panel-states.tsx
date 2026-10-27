// What an Octopus tab shows before its read answers, and when the viewer
// may not read it at all. A failed read is a QueryError with a retry.
import { LockIcon } from "lucide-react"

import { statusOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"

/** PanelLoading is a tab's skeleton: a strip and the cards under it. */
export function PanelLoading({ label }: { label: string }) {
  return (
    <div aria-busy="true" aria-label={label} className="grid gap-3.5">
      <Skeleton className="h-14 rounded-[14px]" />
      <Skeleton className="h-40 rounded-xl" />
      <Skeleton className="h-28 rounded-xl" />
    </div>
  )
}

/** Denied reads differently from a failure: there's nothing to retry. */
export function Denied({ title }: { title: string }) {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LockIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>
          Your role doesn&apos;t include reading this project&apos;s
          infrastructure. An organization owner can give you access.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

/**
 * ReadFailed is what a tab shows when its read didn't answer: Denied for a
 * missing permission, otherwise the error with a retry.
 */
export function ReadFailed({
  error,
  retry,
  denied,
  fallback,
}: {
  error: unknown
  retry: () => Promise<unknown>
  denied: string
  fallback: string
}) {
  const status = statusOf(error)
  if (status === 401 || status === 403) return <Denied title={denied} />
  return <QueryError error={error} fallback={fallback} retry={retry} />
}
