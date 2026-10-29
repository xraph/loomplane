// Which ports a hostname can point at. The discovery graph lists every port
// a service declares, but on App Platform only the one the app listens on is
// reachable, and only when the service is public.
import type {
  Binding,
  DNSRecord,
  RouteOperation,
} from "@/client/forge/loomplane/types"

const privateReason =
  "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first."

const noPortReason =
  "This service has no public port on App Platform. Set port in the manifest and deploy it first."

export function routablePorts(
  servicePorts: number[],
  binding?: Binding
): { ports: number[]; reason?: string } {
  if (binding?.provider !== "digitalocean") return { ports: servicePorts }
  let config: { port?: unknown; public?: unknown } | null
  try {
    config = JSON.parse(binding.config)
  } catch {
    return { ports: servicePorts }
  }
  if (!config || typeof config !== "object") return { ports: servicePorts }
  // The backend reads both as text, so "true" and "8080" count.
  if (String(config.public) !== "true")
    return { ports: [], reason: privateReason }
  const port = Number(config.port)
  if (!Number.isInteger(port) || port <= 0)
    return { ports: [], reason: noPortReason }
  return { ports: [port] }
}

/** Every DNS record an operation wants, old single-record operations included. */
export function operationRecords(op: RouteOperation): DNSRecord[] {
  if (op.desiredRecords?.length) return op.desiredRecords
  return op.desiredRecord ? [op.desiredRecord] : []
}
