import type { CloudSQLSourceEndpoint } from "@/client/forge/loomplane/types"
import { Input } from "@/components/ui/input"
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
      <p className="text-xs text-muted-foreground">
        You manage this manual PSC endpoint and its DNS. Use the VPC host
        project and the selected region. Deployment waits for the endpoint and a
        private SQL connection from the worker before publishing credentials.
      </p>
    </div>
  )
}
