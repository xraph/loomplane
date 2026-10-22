import type { ManagedDatabaseProfile } from "@/client/forge/loomplane/types"

export function ManagedDatabaseSettings({
  profile,
}: {
  profile: ManagedDatabaseProfile
}) {
  const settings = [
    ["Compute", profile.computeClass],
    [
      "Disk",
      `${profile.storageGb} GiB ${profile.storageType}${profile.storageEncrypted ? ", encrypted" : ", unencrypted"}`,
    ],
    ["Availability", profile.availability.replaceAll("-", " ")],
    [
      "Access",
      profile.access === "private-vpc"
        ? "Private VPC"
        : profile.access === "cloud-sql-connector"
          ? "Cloud SQL connector"
          : profile.access,
    ],
    [
      "Provider backups",
      `${profile.backupRetentionValue} ${profile.backupRetentionUnit === "count" ? "copies" : "days"}`,
    ],
    [
      "Recovery logs",
      `${profile.pointInTimeRecoveryDays} days of recovery logs`,
    ],
    [
      "Removal backup",
      profile.finalBackup === "snapshot"
        ? "Final snapshot, no Loomplane expiry"
        : `Final backup, ${profile.finalBackupRetentionDays} days`,
    ],
  ]
  return (
    <div className="mb-3 grid gap-2">
      <dl
        aria-label="Requested managed settings"
        className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-4"
      >
        {settings.map(([label, value]) => (
          <div key={label} className="min-w-0">
            <dt className="text-xs text-muted-foreground">{label}</dt>
            <dd className="break-words">{value}</dd>
          </div>
        ))}
      </dl>
      <p className="text-xs text-muted-foreground">
        These settings are creation requests, not proof of a successful restore.
        Native restore and PITR controls are not yet available in Loomplane.
        Review provider charges before deploying.
      </p>
    </div>
  )
}
