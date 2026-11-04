import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import {
  usePlatformCloudRegionsCreate,
  usePlatformCloudRegionsList,
  usePlatformCloudRegionsUpdate,
} from "@/client/forge/loomplane/hooks"
import type { Region } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  errorOf,
  PlatformCloudEmpty,
  PlatformCloudStatus,
} from "@/components/platform-cloud-parts"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"

/** PlatformCloudRegions lists the regions workspaces pick from, with add and close. */
export function PlatformCloudRegions() {
  const regions = useQuery(usePlatformCloudRegionsList)
  const create = useMutation(usePlatformCloudRegionsCreate)
  const update = useMutation(usePlatformCloudRegionsUpdate)
  const invalidate = useInvalidate()
  const [name, setName] = useState("")
  const [displayName, setDisplayName] = useState("")
  const [error, setError] = useState("")
  const [busy, setBusy] = useState<string | null>(null)

  const add = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      await create.mutateAsync({
        body: { name: name.trim(), displayName: displayName.trim() },
      })
      setName("")
      setDisplayName("")
      invalidate(usePlatformCloudRegionsList)
      toast.success("Region added")
    } catch (err) {
      setError(errorOf(err, "The region wasn't added."))
    }
  }

  // The server takes "active" or "closed"; the page calls active open.
  const setStatus = async (r: Region, status: "active" | "closed") => {
    setError("")
    setBusy(r.id)
    try {
      await update.mutateAsync({
        path: { region: r.id },
        body: { status, revision: r.revision },
      })
      toast.success(status === "closed" ? "Region closed" : "Region opened")
    } catch (err) {
      setError(errorOf(err, "The region wasn't changed."))
    } finally {
      // A conflict means the list is stale, so reload it either way.
      invalidate(usePlatformCloudRegionsList)
      setBusy(null)
    }
  }

  const addForm = (
    <form onSubmit={add} className="flex flex-wrap items-center gap-2">
      <Input
        aria-label="Region name"
        className="w-40"
        placeholder="eu-west"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <Input
        aria-label="Region display name"
        className="w-48"
        placeholder="Western Europe"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
      />
      <Button
        type="submit"
        size="sm"
        disabled={create.isPending || !name.trim() || !displayName.trim()}
      >
        Add region
      </Button>
    </form>
  )

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-base font-medium">Regions</h2>
      {(error || regions.status === "error") && (
        <Alert variant="destructive">
          <AlertDescription>
            {error || errorOf(regions.error, "Regions couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      )}
      {!regions.data && regions.status !== "error" && (
        <Skeleton className="h-16 w-full rounded-xl" />
      )}
      {regions.data?.regions.map((r) => (
        <div
          key={r.id}
          className="flex items-center gap-3 rounded-lg border px-4 py-2"
        >
          <div className="min-w-0 flex-1">
            <div className="font-medium">{r.displayName}</div>
            <div className="text-xs text-muted-foreground">{r.name}</div>
          </div>
          <PlatformCloudStatus kind="region" status={r.status} />
          {r.status === "closed" ? (
            <Button
              size="sm"
              variant="outline"
              aria-label={`Open ${r.displayName}`}
              disabled={busy === r.id}
              onClick={() => setStatus(r, "active")}
            >
              Open
            </Button>
          ) : (
            <Button
              size="sm"
              variant="outline"
              aria-label={`Close ${r.displayName}`}
              disabled={busy === r.id}
              onClick={() => setStatus(r, "closed")}
            >
              Close
            </Button>
          )}
        </div>
      ))}
      {regions.data?.regions.length === 0 ? (
        <PlatformCloudEmpty title="No regions yet." action={addForm}>
          Add a region so you can group hosts by location.
        </PlatformCloudEmpty>
      ) : regions.data || regions.status === "error" ? (
        addForm
      ) : null}
    </section>
  )
}
