// The status bar with what Loomplane can report today: the deployment
// engine and its clouds, and whether GitHub is connected.
import {
  useGithubStatus,
  useInfrastructureGet,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { useShell } from "@/components/shell"
import { StatusBar, StatusItem, StatusLight } from "@/components/status-bar"
import { Link } from "@/lib/router"

export function AppStatusBar() {
  const { openCommand, openShortcuts } = useShell()
  const infra = useQuery(useInfrastructureGet)
  const github = useQuery(useGithubStatus)
  const clouds = infra.data?.providers.length ?? 0
  const installs = github.data?.installations.length ?? 0

  return (
    <StatusBar onShortcuts={openShortcuts} onCommand={openCommand}>
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
      {infra.data && (
        <StatusItem
          hint={
            clouds
              ? infra.data.providers.map((p) => p.name).join(", ")
              : "Clouds arrive with deployments"
          }
          className="hidden sm:flex"
        >
          <StatusLight tone={clouds ? "ok" : "idle"} />
          {clouds === 0
            ? "No clouds connected"
            : clouds === 1
              ? "1 cloud"
              : `${clouds} clouds`}
        </StatusItem>
      )}
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
