// Words, units and prices for Cloud capacity. The API counts CPU in
// millicores and memory and storage in bytes; people read vCPU and GiB.
// GPUs are counted per kind under "gpus:<kind>".
import type { CapacityBlock, Expansion } from "@/client/forge/loomplane/types"
import { formatHourly, formatMonthly, gpuTitle, priceKnown } from "@/lib/gpu"

const resourceLabels: Record<string, string> = {
  cpu_millis: "CPU",
  memory_bytes: "Memory",
  storage_bytes: "Storage",
  servers: "Servers",
  frontends: "Frontends",
}

const gpuPrefix = "gpus:"

/** staleText is what a 409 on a capacity write says, before it reloads. */
export const staleText =
  "This changed since you opened it. Reload to see the current version."

/** busyText is what a 409 says when Loomplane holds the record for a moment. */
export const busyText =
  "Loomplane is working on this right now. Try again in a moment."

/**
 * conflictText is what a 409 on a capacity write says. The server answers
 * 409 for a revision that moved and for work already running; only the
 * first asks for a reload.
 */
export function conflictText(err: unknown): string {
  const raw = err instanceof Error ? err.message : ""
  return /running/i.test(raw) ? busyText : staleText
}

/**
 * rolloutOffText is why a deployment waits when this Loomplane server has
 * automatic expansion off and it needs a server.
 * The server and loomctl say the same.
 */
export const rolloutOffText =
  "Automatic expansion is off on this Loomplane server, so no server will be added. Add or adopt one yourself."

function words(raw: string): string {
  return raw.replace(/_/g, " ")
}

/** resourceLabel names a capacity resource, such as "CPU" or "NVIDIA L4 GPUs". */
export function resourceLabel(resource: string): string {
  if (resource.startsWith(gpuPrefix))
    return `${gpuTitle(resource.slice(gpuPrefix.length))} GPUs`
  if (Object.hasOwn(resourceLabels, resource)) return resourceLabels[resource]
  const w = words(resource)
  return w.charAt(0).toUpperCase() + w.slice(1)
}

// Up to two decimals, without trailing zeros: 4, 0.5, 1.25.
function trim(n: number, digits = 2): string {
  return Number(n.toFixed(digits)).toLocaleString("en-US")
}

/** formatAmount is an amount of a resource in people's units. */
export function formatAmount(resource: string, n: number): string {
  if (resource === "cpu_millis") return `${trim(n / 1000)} vCPU`
  if (resource.endsWith("_bytes")) {
    if (n !== 0 && Math.abs(n) < 2 ** 30) return `${trim(n / 2 ** 20, 0)} MiB`
    return `${trim(n / 2 ** 30, 1)} GiB`
  }
  return n.toLocaleString("en-US")
}

/** blockText says in one sentence why a block holds a deployment back. */
export function blockText(b: CapacityBlock): string {
  const label = resourceLabel(b.resource)
  switch (b.reason) {
    case "hard_limit": {
      const counted = formatAmount(b.resource, b.counted)
      const limit = formatAmount(b.resource, b.limit)
      const requested = formatAmount(b.resource, b.requested)
      return `${label} is at ${b.poolId ? "this pool's" : "this Cloud's"} limit: ${counted} of ${limit} counted, ${requested} more needed.`
    }
    case "expansion_approval":
      return "Waiting for a Cloud manager to approve a new server."
    case "provisioning":
      return b.resource === "gpu_runtime"
        ? "Waiting for the new GPU server's NVIDIA runtime."
        : "A new server is being created."
    case "stale_inventory":
      return `Loomplane can't confirm current ${label} usage, so it won't add capacity until a refresh succeeds.`
    case "provider_quota":
      return "The provider account's quota is full."
    case "provider_failure":
      return "The provider couldn't create the server. Loomplane will retry."
    case "placement_constraint":
      return "No allowed server size fits this workload."
    case "review_required":
      return b.resource === "rollout"
        ? rolloutOffText
        : "This deployment's plan changed. Review it again."
    default:
      return words(b.reason)
  }
}

/**
 * expansionPrice is what a new server costs while it runs, or "price
 * unknown". An unknown price is never shown as $0.
 */
export function expansionPrice(
  e: Pick<Expansion, "estimatedHourlyUsd" | "hourlyPriceKnown">
): string {
  if (!priceKnown(e.estimatedHourlyUsd, e.hourlyPriceKnown))
    return formatHourly(0, false)
  const usd = e.estimatedHourlyUsd
  return `${formatHourly(usd, true)}, ${formatMonthly(usd)} while it runs`
}

export const vcpuToMillis = (v: number) => Math.round(v * 1000)
export const gibToBytes = (g: number) => Math.round(g * 2 ** 30)
