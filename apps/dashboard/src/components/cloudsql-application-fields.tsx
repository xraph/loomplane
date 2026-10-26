import type { CloudSQLApplicationNetwork } from "@/client/forge/loomplane/types"
import { Input } from "@/components/ui/input"

export function CloudSQLApplicationFields({
  name,
  value,
  disabled,
  onChange,
}: {
  name: string
  value: CloudSQLApplicationNetwork
  disabled: boolean
  onChange: (value: CloudSQLApplicationNetwork) => void
}) {
  return (
    <fieldset className="grid min-w-0 gap-2" disabled={disabled}>
      <legend className="mb-2 text-[13px] font-medium">
        Cloud SQL private route (PSC)
      </legend>
      <div className="grid min-w-0 gap-2 sm:grid-cols-2">
        <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
          VPC network
          <Input
            className="h-8 min-w-0 text-xs"
            aria-label={`PSC network for ${name}`}
            value={value.network}
            placeholder="projects/host-project/global/networks/apps"
            onChange={(e) =>
              onChange({
                ...value,
                network: e.target.value,
                consumerProject: e.target.value.split("/")[1] ?? "",
              })
            }
          />
        </label>
        <label className="grid min-w-0 gap-1.5 text-[12.5px] font-medium">
          Regional subnet
          <Input
            className="h-8 min-w-0 text-xs"
            aria-label={`PSC subnet for ${name}`}
            value={value.subnetwork}
            placeholder="projects/host-project/regions/us-central1/subnetworks/apps"
            onChange={(e) => onChange({ ...value, subnetwork: e.target.value })}
          />
        </label>
      </div>
      <p className="text-xs text-muted-foreground">
        Use the database endpoint's VPC and a subnet in this service's region.
        Leave both empty to restore the service route. You still review the
        route before deployment.
      </p>
    </fieldset>
  )
}
