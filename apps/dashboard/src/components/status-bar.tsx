// A status bar along the bottom, the way an editor has one: the state of
// the machinery under Loomplane, readable at a glance and quiet otherwise.
// Each app fills the left side with what it knows.
import { CommandIcon } from "lucide-react"
import type { ReactNode } from "react"

import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

/** StatusItem is one fact in the bar, explained on hover. */
export function StatusItem({
  children,
  hint,
  className,
}: {
  children: ReactNode
  hint: string
  className?: string
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="xs"
            className={cn(
              "h-6 gap-1.5 px-1.5 font-normal text-muted-foreground",
              className
            )}
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent side="top">{hint}</TooltipContent>
    </Tooltip>
  )
}

/** StatusLight is the small light in front of a status item. */
export function StatusLight({ tone }: { tone: "ok" | "warn" | "idle" }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "size-1.5 rounded-full",
        tone === "ok" && "bg-overdye",
        tone === "warn" && "bg-weld",
        tone === "idle" && "bg-muted-foreground/40"
      )}
    />
  )
}

export function StatusBar({
  children,
  onShortcuts,
  onCommand,
  version,
}: {
  children?: ReactNode
  onShortcuts: () => void
  onCommand: () => void
  version?: string
}) {
  return (
    <footer className="sticky bottom-0 z-20 mt-auto flex h-8 min-w-0 shrink-0 items-center gap-1 overflow-hidden border-t bg-background/85 px-2 text-xs text-muted-foreground backdrop-blur-xl md:px-3">
      {children}
      <div className="ml-auto flex shrink-0 items-center gap-1">
        <Button
          variant="ghost"
          size="xs"
          onClick={onShortcuts}
          className="hidden h-6 px-1.5 font-normal text-muted-foreground xl:inline-flex"
        >
          ? Shortcuts
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={onCommand}
          className="h-6 gap-1 px-1.5 font-normal text-muted-foreground"
        >
          <CommandIcon />K
          <span className="hidden lg:inline"> Command menu</span>
        </Button>
        {version && (
          <span className="hidden px-1.5 font-mono lg:inline">{version}</span>
        )}
      </div>
    </footer>
  )
}
