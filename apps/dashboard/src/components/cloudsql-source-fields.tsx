import type { CloudSQLSourceEndpoint } from "@/client/forge/loomplane/types"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"

export function CloudSQLSourceFields({
  value,
  onChange,
  disabled,
}: {
  value: CloudSQLSourceEndpoint
  onChange: (value: CloudSQLSourceEndpoint) => void
  disabled: boolean
}) {
  const provision = value.provisionSubnetwork !== undefined
  return (
    <div className="grid gap-2">
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="grid min-w-0 gap-1.5">
          <Label htmlFor="cloudsql-source-network">VPC network</Label>
          <Input
            id="cloudsql-source-network"
            value={value.network}
            disabled={disabled}
            required
            placeholder="projects/host-project/global/networks/apps"
            onChange={(event) =>
              onChange({
                ...value,
                network: event.target.value,
                consumerProject: event.target.value.split("/")[1] ?? "",
              })
            }
          />
        </div>
        <div className="grid min-w-0 gap-1.5">
          <Label htmlFor="cloudsql-source-rule">PSC forwarding rule</Label>
          <Input
            id="cloudsql-source-rule"
            value={value.forwardingRule}
            disabled={disabled}
            required
            placeholder="projects/host-project/regions/us-central1/forwardingRules/database"
            onChange={(event) =>
              onChange({ ...value, forwardingRule: event.target.value })
            }
          />
        </div>
      </div>
      <label className="flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={provision}
          disabled={disabled}
          onCheckedChange={(checked) => {
            const next = { ...value }
            if (checked) next.provisionSubnetwork = ""
            else delete next.provisionSubnetwork
            onChange(next)
          }}
        />
        Create endpoint and private DNS
      </label>
      {provision && (
        <div className="grid min-w-0 gap-1.5">
          <Label htmlFor="cloudsql-source-subnet">PSC subnet</Label>
          <Input
            id="cloudsql-source-subnet"
            value={value.provisionSubnetwork ?? ""}
            disabled={disabled}
            required
            placeholder="projects/host-project/regions/us-central1/subnetworks/psc"
            onChange={(event) =>
              onChange({ ...value, provisionSubnetwork: event.target.value })
            }
          />
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        {provision
          ? "Loomplane creates the named endpoint in this subnet and its private DNS. Removing the database also removes these owned resources."
          : "You manage this manual PSC endpoint and its DNS."}{" "}
        Use the VPC host project and selected region. Deployment verifies a
        private SQL connection before publishing credentials.
      </p>
    </div>
  )
}
