import { LayersIcon } from "lucide-react"
import { statusOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { Link } from "@/lib/router"

export function FleetReadState({
  projectID,
  error,
  loading,
  retry,
}: {
  projectID: string
  error?: unknown
  loading?: boolean
  retry: () => Promise<unknown>
}) {
  if (loading)
    return (
      <div role="status" aria-busy="true" className="grid gap-3">
        <span className="text-sm text-muted-foreground">
          Loading fleet state
        </span>
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  const status = statusOf(error)
  const denied = status === 401 || status === 403
  const missing = status === 404
  return (
    <ZeroState
      variant="illustrated"
      preview={false}
      role={denied || missing ? "status" : "alert"}
    >
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LayersIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>
          {denied
            ? "Fleet access denied"
            : missing
              ? "Fleet resource unavailable"
              : "Could not load fleet state"}
        </ZeroStateTitle>
        <ZeroStateDescription>
          {denied
            ? "Your current permissions do not allow this view."
            : missing
              ? "This resource may have been removed or belongs to another project."
              : "Try again to load the current state. No operation was submitted."}
        </ZeroStateDescription>
      </ZeroStateHeader>
      <ZeroStateContent>
        {!denied && !missing && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => void retry().catch(() => undefined)}
          >
            Retry
          </Button>
        )}
        <Link
          to="/projects/$id"
          params={{ id: projectID }}
          className="text-primary underline underline-offset-4"
        >
          Back to project
        </Link>
      </ZeroStateContent>
    </ZeroState>
  )
}
