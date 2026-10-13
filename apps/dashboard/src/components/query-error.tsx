import { messageOf } from "@/client/errors"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"

/** A failed read keeps its explanation and retry beside the affected view. */
export function QueryError({
  error,
  fallback,
  retry,
  label = "Retry",
}: {
  error: unknown
  fallback: string
  retry: () => Promise<unknown>
  label?: string
}) {
  return (
    <Alert variant="destructive">
      <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
        <span>{messageOf(error, fallback)}</span>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void retry().catch(() => undefined)}
        >
          {label}
        </Button>
      </AlertDescription>
    </Alert>
  )
}
