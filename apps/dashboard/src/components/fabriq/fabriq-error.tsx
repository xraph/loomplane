import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { unreachableReason, unreachableText } from "@/lib/fabriq"

/**
 * FabriqError is a failed read of Fabriq's tenants or keys. When the API
 * says Fabriq itself can't be reached, it says so with the reason and
 * offers to try again; anything else reads like any failed query.
 */
export function FabriqError({
  error,
  fallback,
  retry,
}: {
  error: unknown
  fallback: string
  retry: () => Promise<unknown>
}) {
  const reason = unreachableReason(error)
  if (reason === undefined)
    return <QueryError error={error} fallback={fallback} retry={retry} />
  return (
    <Alert variant="destructive">
      <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
        <span>{unreachableText(reason)}</span>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void retry().catch(() => undefined)}
        >
          Retry
        </Button>
      </AlertDescription>
    </Alert>
  )
}
