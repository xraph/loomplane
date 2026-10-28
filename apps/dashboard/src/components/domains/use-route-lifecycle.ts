// A hostname's route, from preview to removal, over the generated route
// hooks. Every change refreshes the views that list hostnames.
import { useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useDomainRoutesApply,
  useDomainRoutesList,
  useDomainRoutesPreview,
  useDomainRoutesRemove,
  useDomainRoutesRetry,
  useDomainsOverview,
  useDomainsProject,
} from "@/client/forge/loomplane/hooks"
import type {
  CloudinfraRoute,
  RouteInput,
  RouteOperation,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"

export type Route = CloudinfraRoute

// The server's bare invalid-request text names nothing a person can fix.
// In front of a reason it's noise, and the reason is kept as written: it
// often starts with a hostname, which mustn't be capitalized.
function refusal(err: unknown, fallback: string): string {
  const raw = err instanceof Error ? err.message : ""
  if (/^invalid infrastructure request\.?$/i.test(raw)) return fallback
  const reason = raw.replace(/^invalid infrastructure request:\s*/i, "")
  if (reason !== raw && reason)
    return /[.!?]$/.test(reason) ? reason : `${reason}.`
  return messageOf(err, fallback)
}

export function useRouteLifecycle(projectId: string): {
  preview(input: RouteInput): Promise<RouteOperation>
  apply(op: RouteOperation): Promise<RouteOperation>
  remove(route: Route): Promise<RouteOperation>
  retry(operationId: string): Promise<RouteOperation>
  pending: boolean
  error: string
} {
  const previewing = useMutation(useDomainRoutesPreview)
  const applying = useMutation(useDomainRoutesApply)
  const removing = useMutation(useDomainRoutesRemove)
  const retrying = useMutation(useDomainRoutesRetry)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")

  const run = async (
    call: () => Promise<RouteOperation>,
    fallback: string,
    changes: boolean
  ) => {
    setError("")
    try {
      const op = await call()
      if (changes) {
        invalidate(useDomainsProject)
        invalidate(useDomainRoutesList)
        invalidate(useDomainsOverview)
      }
      return op
    } catch (err) {
      setError(refusal(err, fallback))
      throw err
    }
  }

  return {
    // A preview stores a review and changes nothing else.
    preview: (input) =>
      run(
        () => previewing.mutateAsync({ path: { projectId }, body: input }),
        "Check the hostname and port, then preview again.",
        false
      ),
    apply: (op) =>
      run(
        () =>
          applying.mutateAsync({
            path: { projectId, operationId: op.id },
            body: { review: op.review },
          }),
        "The route wasn't applied.",
        true
      ),
    remove: (route) =>
      run(
        () =>
          removing.mutateAsync({
            path: { projectId, routeId: route.id },
            body: { revision: route.revision },
          }),
        "The route wasn't removed.",
        true
      ),
    retry: (operationId) =>
      run(
        () => retrying.mutateAsync({ path: { projectId, operationId } }),
        "The route wasn't retried.",
        true
      ),
    pending:
      previewing.isPending ||
      applying.isPending ||
      removing.isPending ||
      retrying.isPending,
    error,
  }
}
