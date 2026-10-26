import { componentName, type CatalogPanelProps } from "@/lib/catalog-kinds"

/**
 * fabriqDeployedLabel is the state a Fabriq's header shows when nothing has
 * drifted, in the tone the Stack gives it. It is Running only while the process that serves it, the server
 * or the library's worker, is deployed, the same test the Stack uses for
 * its card, so the header never says Running above a Stack that says Not
 * running.
 */
export function fabriqDeployedLabel({
  resource,
  kind,
  components,
}: CatalogPanelProps): { label: string; tone: "live" | "waiting" } {
  const process = componentName(
    resource,
    kind === "fabriq-library" ? "worker" : "server"
  )
  const deployed = components.find((b) => b.name === process)
  return deployed && deployed.drift !== "missing"
    ? { label: "Running", tone: "live" }
    : { label: "Not running", tone: "waiting" }
}
