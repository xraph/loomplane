import { componentName, type CatalogPanelProps } from "@/lib/catalog-kinds"

/**
 * fabriqDeployedLabel is the state a Fabriq's header shows when nothing has
 * drifted. It is Running only while the process that serves it, the server
 * or the library's worker, is deployed, the same test the Stack uses for
 * its card, so the header never says Running above a Stack that says Not
 * running.
 */
export function fabriqDeployedLabel({
  resource,
  kind,
  components,
}: CatalogPanelProps): string {
  const process = componentName(
    resource,
    kind === "fabriq-library" ? "worker" : "server"
  )
  const deployed = components.find((b) => b.name === process)
  return deployed && deployed.drift !== "missing" ? "Running" : "Not running"
}
