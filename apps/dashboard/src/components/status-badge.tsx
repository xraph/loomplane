import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

/** isReady reports whether a project has finished setting up. */
export function isReady(status: string): boolean {
  return status === "ready"
}

/** StatusBadge shows a project's status as a thread: weld while it's set up, overdye once ready. */
export function StatusBadge({ status }: { status: string }) {
  const ready = isReady(status)
  return (
    <Badge variant="outline" className="gap-1.5">
      <span
        aria-hidden="true"
        className={cn(
          "h-1.5 w-3 rounded-full",
          ready
            ? "bg-overdye"
            : "animate-pulse bg-weld motion-reduce:animate-none"
        )}
      />
      {ready ? "Ready" : "Setting up"}
    </Badge>
  )
}
