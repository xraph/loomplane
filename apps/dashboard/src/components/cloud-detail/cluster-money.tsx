import type { MoneyEstimate } from "@/client/forge/loomplane/types"
import { Row, Rows } from "@/components/cloud-targets/infrastructure-ui"

export function observedTime(value?: string) {
  if (!value || value.startsWith("0001-")) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date.toLocaleString()
}

export function exactUSD(micro?: string) {
  if (!micro || !/^\d+$/.test(micro)) return "Unknown"
  const amount = BigInt(micro)
  return `$${amount / 1000000n}.${(amount % 1000000n).toString().padStart(6, "0")}`
}
export const costCategory = (category = "") =>
  ({
    nodes: "Nodes",
    control_plane: "Control plane",
    persistent_storage: "Persistent storage",
    load_balancers: "Load balancers",
    network_egress: "Network traffic",
    pending_effect: "Pending changes",
    retained_resource: "Retained resources",
    associated_inventory: "Associated resources",
  })[category] ??
  (category.replaceAll("_", " ") || "Unclassified cost")

export function ClusterMoney({ estimate }: { estimate?: MoneyEstimate }) {
  if (!estimate)
    return (
      <p className="text-xs text-muted-foreground">
        Cost estimate unavailable. Unknown charges are not zero.
      </p>
    )
  return (
    <div className="grid gap-2" aria-label="Cost breakdown">
      <p className="text-sm">
        {estimate.known ? "Estimated" : "Known portion"}:{" "}
        {exactUSD(estimate.hourlyMicroUsd)} / hour ·{" "}
        {exactUSD(estimate.horizonMicroUsd)} over{" "}
        {estimate.horizonSeconds / 3600} hours
      </p>
      <Rows>
        {(estimate.components ?? []).map((part, i) => (
          <Row key={i}>
            <span>{costCategory(part.category)}</span>
            <span>
              {exactUSD(part.hourlyMicroUsd)} / hour · quantity {part.quantity}
              {observedTime(part.priceCheckedAt)
                ? ` · checked ${observedTime(part.priceCheckedAt)}`
                : " · freshness unavailable"}
            </span>
          </Row>
        ))}
      </Rows>
      {!estimate.known && (
        <p className="text-xs">
          Unknown charges remain outside this subtotal. A manual acknowledgement
          covers only this reviewed scope. Automatic growth requires known
          costs.
        </p>
      )}
    </div>
  )
}
