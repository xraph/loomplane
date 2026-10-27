// How a push to Octopus is reported, and the push the Discovery and
// Settings tabs run again by hand.
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useOctopusBoard,
  useOctopusDiscovery,
  useOctopusStatus,
  useOctopusSync,
} from "@/client/forge/loomplane/hooks"
import type { SyncResult } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"

/**
 * reportSync tells the person how a push went when it didn't simply
 * apply: an error for a failure, a note otherwise. The prefix says what
 * happened first, such as "Saved. " for a route that was stored.
 */
export function reportSync(sync: SyncResult | undefined, prefix = "") {
  if (!sync || sync.state === "applied" || !sync.message) return
  if (sync.state === "failed") toast.error(`${prefix}${sync.message}`)
  else toast(`${prefix}${sync.message}`)
}

/**
 * usePushRoutes pushes everything Loomplane keeps for a gateway to
 * Octopus again; on Docker that registers every FARP service again too.
 * The board, status and discovery are read again after it, however it
 * went.
 */
export function usePushRoutes(at: {
  projectId: string
  environmentId: string
  resource: string
}): { push(): Promise<void>; pending: boolean } {
  const sync = useMutation(useOctopusSync)
  const invalidate = useInvalidate()
  return {
    push: async () => {
      try {
        // The generated mutation doesn't carry the response's type.
        reportSync((await sync.mutateAsync({ path: at })) as SyncResult)
      } catch (err) {
        toast.error(messageOf(err, "Octopus wasn't synced."))
      }
      invalidate(useOctopusBoard)
      invalidate(useOctopusStatus)
      invalidate(useOctopusDiscovery)
    },
    pending: sync.isPending,
  }
}
