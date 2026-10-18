import { useCallback, useEffect, useSyncExternalStore } from "react"

// Pending requests are transient UI state. Placement and account choices
// remain on the server; this only coordinates the tab and global dialog.
const counts = new Map<string, number>()
const listeners = new Set<() => void>()
function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function useDeploymentWrites(projectId: string) {
  const count = useSyncExternalStore(
    subscribe,
    () => counts.get(projectId) ?? 0,
    () => 0
  )
  const onPendingChange = useCallback(
    (pending: boolean) => {
      const next = Math.max(
        0,
        (counts.get(projectId) ?? 0) + (pending ? 1 : -1)
      )
      if (next) counts.set(projectId, next)
      else counts.delete(projectId)
      for (const listener of listeners) listener()
    },
    [projectId]
  )
  return { busy: count > 0, onPendingChange }
}

// Unsaved form edits count as active writes until they are saved, undone
// or unmounted. Cleanup keeps project/environment changes balanced.
export function useDeploymentDraft(
  dirty: boolean,
  onPendingChange?: (pending: boolean) => void
) {
  useEffect(() => {
    if (!dirty) return
    onPendingChange?.(true)
    return () => onPendingChange?.(false)
  }, [dirty, onPendingChange])
}
