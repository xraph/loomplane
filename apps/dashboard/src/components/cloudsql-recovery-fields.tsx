import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function cloudSQLRecoverySettings(value: Record<string, string>) {
  const network = value.cloud_sql_recovery_network?.trim()
  return network ? { cloud_sql_recovery_network: network } : {}
}

export function CloudSQLRecoveryFields({
  value,
  onChange,
}: {
  value: Record<string, string>
  onChange: (value: Record<string, string>) => void
}) {
  return (
    <details className="text-sm">
      <summary className="cursor-pointer">
        Private Cloud SQL recovery (optional)
      </summary>
      <p className="my-2 text-xs text-muted-foreground">
        Use the recovery worker&apos;s VPC with an existing Cloud SQL PSC
        service connection policy. Each copy still needs endpoint, DNS and SQL
        checks.
      </p>
      <div className="grid gap-1.5">
        <Label htmlFor="connection-cloud-sql-recovery-network">
          Recovery consumer VPC
        </Label>
        <Input
          id="connection-cloud-sql-recovery-network"
          value={value.cloud_sql_recovery_network ?? ""}
          onChange={(event) =>
            onChange({
              ...value,
              cloud_sql_recovery_network: event.target.value,
            })
          }
          placeholder="projects/PROJECT/global/networks/NETWORK"
          autoComplete="off"
          spellCheck={false}
        />
      </div>
    </details>
  )
}
