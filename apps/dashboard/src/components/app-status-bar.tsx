// The status bar with what Loomplane can report today: the deployment
// engine, the clouds it deploys to, and whether GitHub is connected.
import {
  useConnectionsList,
  useGithubStatus,
  useInfrastructureGet,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { useShell } from "@/components/shell"
import { StatusBar, StatusItem, StatusLight } from "@/components/status-bar"
import { Link } from "@/lib/router"
import { useRuns } from "@/lib/use-runs"
import { useLiveState } from "@/lib/realtime-state"

export function AppStatusBar() {
  const live = useLiveState()
  const { openCommand, openShortcuts } = useShell()
  const infra = useQuery(useInfrastructureGet)
  const github = useQuery(useGithubStatus)
  const connections = useQuery(useConnectionsList)
  const all = connections.data?.connections ?? []
  const clouds = all.length
  const unready = all.filter((c) => c.status !== "ready").length
  const installs = github.data?.installations.length ?? 0
  const { runs } = useRuns()
  const deploying = runs.filter((r) => r.status === "running").length
  const waiting = runs.filter((r) => r.status === "waiting").length

  return (
    <StatusBar onShortcuts={openShortcuts} onCommand={openCommand}>
      <StatusItem
        hint={
          live.lastSync
            ? `Last synchronized ${new Date(live.lastSync).toLocaleTimeString()} via ${live.transport.toUpperCase()}`
            : "Waiting for an authenticated status stream"
        }
      >
        <StatusLight tone={live.status === "live" ? "ok" : "warn"} />
        {live.status === "live"
          ? "Live"
          : live.status === "denied"
            ? "Access denied"
            : live.status === "reconnecting"
              ? "Reconnecting"
              : "Status stale"}
      </StatusItem>
      {infra.data && (
        <StatusItem
          hint={`Ctrl Plane keeps deployment state in ${infra.data.store}`}
        >
          <StatusLight tone="ok" />
          {infra.data.engine}
        </StatusItem>
      )}
      {infra.status === "error" && (
        <StatusItem hint="The infrastructure status didn't load">
          <StatusLight tone="warn" />
          Engine unknown
        </StatusItem>
      )}
      {connections.data && (
        <StatusItem
          hint={
            clouds === 0
              ? "Connect Google Cloud, AWS, Vercel or Render under Integrations to deploy"
              : unready > 0
                ? `${unready} of them ${unready === 1 ? "needs" : "need"} attention under Integrations`
                : all.map((c) => c.name).join(", ")
          }
          className="hidden sm:flex"
        >
          <Link to="/integrations" className="flex items-center gap-1.5">
            <StatusLight
              tone={clouds === 0 ? "idle" : unready > 0 ? "warn" : "ok"}
            />
            {clouds === 0
              ? "No clouds connected"
              : clouds === 1
                ? "1 cloud"
                : `${clouds} clouds`}
          </Link>
        </StatusItem>
      )}
      <StatusItem
        hint={
          waiting
            ? `${waiting} ${waiting === 1 ? "plan waits" : "plans wait"} for approval`
            : "Plans and deployments running right now"
        }
        className="hidden sm:flex"
      >
        <Link
          to="/deployments"
          search={deploying || waiting ? { status: "live" } : {}}
          className="flex items-center gap-1.5"
        >
          <StatusLight tone={deploying || waiting ? "warn" : "idle"} />
          {deploying === 0 && waiting === 0
            ? "Nothing deploying"
            : `${deploying} deploying${waiting ? `, ${waiting} waiting` : ""}`}
        </Link>
      </StatusItem>
      {github.data && (
        <StatusItem
          hint={
            installs
              ? "Loomplane can read the repositories you chose on GitHub"
              : "Connect GitHub under Integrations to analyze private repositories"
          }
          className="hidden md:flex"
        >
          <Link to="/integrations" className="flex items-center gap-1.5">
            <StatusLight tone={installs ? "ok" : "idle"} />
            {installs ? "GitHub connected" : "GitHub not connected"}
          </Link>
        </StatusItem>
      )}
    </StatusBar>
  )
}
