import type { MoneyEstimate } from "@/client/forge/loomplane/types"
import {
  Cell,
  CloudTable,
  CloudTableRow,
  hourlyCost,
} from "@/components/cloud-targets/cloud-page"
import { formatDate } from "@/lib/format"

/** observedTime is a provider timestamp as a date, unless it was never set. */
export function observedTime(value?: string) {
  if (!value || value.startsWith("0001-")) return undefined
  return Number.isNaN(new Date(value).getTime()) ? undefined : formatDate(value)
}

/** microUSD reads an hourly price in micro-dollars, if one is known. */
export function microUSD(micro?: string): number | undefined {
  if (!micro || !/^\d+$/.test(micro)) return undefined
  return Number(micro) / 1_000_000
}

/** microCost is an hourly micro-dollar price in the standard money format. */
export function microCost(micro?: string, unknown = "Price unknown") {
  const usd = microUSD(micro)
  return usd === undefined ? unknown : hourlyCost(usd)
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

const costColumns = "minmax(0,1fr) minmax(0,1.4fr) 80px minmax(0,1fr)"

/** ClusterMoney is a reviewed change's cost: the total, then each part. */
export function ClusterMoney({ estimate }: { estimate?: MoneyEstimate }) {
  if (!estimate)
    return (
      <p className="text-[12.5px] text-muted-foreground">
        Cost estimate unavailable. Unknown charges are not zero.
      </p>
    )
  const horizon = microUSD(estimate.horizonMicroUsd)
  return (
    <div className="grid gap-2.5" aria-label="Cost breakdown">
      <p className="text-[13px]">
        {estimate.known ? "Estimated" : "Known portion"}:{" "}
        {microCost(estimate.hourlyMicroUsd)}
        {horizon !== undefined &&
          estimate.horizonSeconds > 0 &&
          ` · $${horizon.toFixed(2)} over ${estimate.horizonSeconds / 3600} hours`}
      </p>
      {!!estimate.components?.length && (
        <CloudTable
          label="Cost parts"
          columns={costColumns}
          head={["Part", "Cost", "Quantity", "Price checked"]}
          minWidth={480}
        >
          {estimate.components.map((part, i) => (
            <CloudTableRow key={i}>
              <Cell>{costCategory(part.category)}</Cell>
              <Cell className="tabular-nums">
                {microCost(part.hourlyMicroUsd)}
              </Cell>
              <Cell className="tabular-nums">{part.quantity}</Cell>
              <Cell className="text-muted-foreground">
                {observedTime(part.priceCheckedAt) ?? "Not checked"}
              </Cell>
            </CloudTableRow>
          ))}
        </CloudTable>
      )}
      {!estimate.known && (
        <p className="text-[12.5px] text-muted-foreground">
          Unknown charges remain outside this subtotal. A manual acknowledgement
          covers only this reviewed scope. Automatic growth requires known
          costs.
        </p>
      )}
    </div>
  )
}
