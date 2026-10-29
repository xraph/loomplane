import type { Connection } from "@/client/forge/loomplane/types"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"

export function clusterSetupWaiting(connection: Connection): boolean {
  return (
    connection.cloud === "kubernetes" &&
    connection.status === "incomplete" &&
    connection.details.registry_setup_status === "waiting"
  )
}

export function ClusterBuildSetup({ connection }: { connection: Connection }) {
  if (
    connection.cloud !== "kubernetes" ||
    !connection.details.registry_hostname
  )
    return null
  const hostname = String(connection.details.registry_hostname)
  const address = String(connection.details.ingress_address ?? "")
  return (
    <div className="grid min-w-0 gap-2 text-xs">
      {address && (
        <p className="break-words text-muted-foreground">
          Point <code>{hostname}</code> to <code>{address}</code> in DNS.
        </p>
      )}
      {connection.status === "incomplete" && (
        <Alert className="border-weld/50">
          <AlertTitle>
            {clusterSetupWaiting(connection)
              ? "Waiting for cluster setup"
              : "Cluster requirements"}
          </AlertTitle>
          <AlertDescription className="grid gap-1">
            <ul className="grid gap-1 break-words">
              {connection.missing.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
            <p>
              Check again to refresh readiness. Saving the connection again
              retries automatic setup.
            </p>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
