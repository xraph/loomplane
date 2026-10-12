// The mock's status bar: the shared one, with made-up machinery state.
import {
  StatusBar as SharedStatusBar,
  StatusItem,
} from "@/components/status-bar"
import { cn } from "@/lib/utils"
import { providers } from "@/mock/data"
import { useShell } from "@/mock/shell"
import { useStore } from "@/mock/store"

export function StatusBar() {
  const { deployments, now } = useStore()
  const { openCommand, openShortcuts } = useShell()
  const busy = deployments.filter((d) => d.status === "running").length
  const degraded = providers.filter((p) => p.status !== "connected")
  // Ctrl Plane reconciles every 15 seconds in the mock.
  const synced = Math.floor((now / 1000) % 15)
  return (
    <SharedStatusBar
      onShortcuts={openShortcuts}
      onCommand={openCommand}
      version="v0.4.0-mock"
    >
      <StatusItem
        hint={
          degraded.length
            ? `${degraded.map((p) => p.name).join(", ")} is slow to answer`
            : "Every cloud connection answers"
        }
      >
        <span
          className={cn(
            "size-1.5 rounded-full",
            degraded.length ? "bg-weld" : "bg-overdye"
          )}
        />
        {degraded.length
          ? `${degraded.length} cloud degraded`
          : "All systems normal"}
      </StatusItem>
      <StatusItem
        hint="Dispatch workers running deployment steps"
        className="hidden sm:flex"
      >
        Dispatch {busy}/4 busy
      </StatusItem>
      <StatusItem
        hint="Ctrl Plane compares real infrastructure with recorded state"
        className="hidden md:flex"
      >
        Ctrl Plane synced {synced}s ago
      </StatusItem>
    </SharedStatusBar>
  )
}
