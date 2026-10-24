import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

const fields = [
  ["rds_recovery_vpc", "Recovery VPC", "vpc-…"],
  ["rds_recovery_subnet_group", "Recovery subnet group", "recovery-private"],
  ["rds_recovery_security_group", "Recovery security group", "sg-…"],
  ["rds_recovery_verifier_group", "Verifier security group", "sg-…"],
] as const

export function rdsRecoverySettings(value: Record<string, string>) {
  return Object.fromEntries(
    fields.map(([key]) => [key, (value[key] ?? "").trim()]).filter(([, v]) => v)
  )
}

export function RDSRecoveryFields({
  value,
  onChange,
}: {
  value: Record<string, string>
  onChange: (value: Record<string, string>) => void
}) {
  return (
    <details className="text-sm">
      <summary className="cursor-pointer">
        Private RDS recovery (optional)
      </summary>
      <p className="my-2 text-xs text-muted-foreground">
        Configure all four fields for a private recovery worker route. Allow
        PostgreSQL ingress only from the separate verifier group. Each copy
        still needs provider checks and SQL verification before use.
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        {fields.map(([key, label, placeholder]) => (
          <div key={key} className="grid gap-1.5">
            <Label htmlFor={`connection-${key}`}>{label}</Label>
            <Input
              id={`connection-${key}`}
              value={value[key] ?? ""}
              onChange={(event) =>
                onChange({ ...value, [key]: event.target.value })
              }
              placeholder={placeholder}
              spellCheck={false}
            />
          </div>
        ))}
      </div>
    </details>
  )
}
