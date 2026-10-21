import type {
  Connection,
  ManagedDatabaseProfile,
  Plan,
  Provider,
} from "@/client/forge/loomplane/types"

export function managedDatabaseFixture(target: "rds" | "cloud-sql") {
  const aws = target === "rds"
  const profile: ManagedDatabaseProfile = {
    id: `postgresql-17-${target}`,
    revision: "1",
    provider: target,
    kind: "postgres",
    version: "17",
    computeClass: aws ? "db.t4g.micro" : "db-custom-1-3840",
    storageGb: aws ? 20 : 10,
    storageType: aws ? "gp3" : "PD_SSD",
    storageEncrypted: true,
    availability: "single-zone",
    access: aws ? "private-vpc" : "cloud-sql-connector",
    backupRetentionValue: 7,
    backupRetentionUnit: aws ? "days" : "count",
    pointInTimeRecoveryDays: 7,
    finalBackup: aws ? "snapshot" : "final-backup",
    finalBackupRetentionDays: aws ? 0 : 30,
    retainAutomatedBackups: aws,
  }
  const provider: Provider = {
    name: target,
    title: aws ? "Amazon RDS" : "Google Cloud SQL",
    cloud: aws ? "aws" : "gcp",
    model: "managed",
    available: true,
    description: "Managed PostgreSQL",
    scaling: false,
    regions: aws ? ["us-east-1", "us-west-2"] : ["us-central1", "europe-west1"],
    provisions: [
      {
        kind: "postgres",
        creates: "Managed PostgreSQL instance",
        versions: ["17"],
        managedProfiles: [profile],
      },
    ],
  }
  const connection = {
    id: "selected",
    name: "Selected account",
    cloud: provider.cloud,
    status: "ready",
    orgId: "org",
    account: aws ? "123456789012" : "project-a",
    accountIdentity: "reviewed-account",
    details: aws ? { region: "us-east-1" } : { project_id: "project-a" },
    createdBy: "user",
    createdAt: "2026-10-21T00:00:00Z",
    updatedAt: "2026-10-21T00:00:00Z",
    message: "",
    missing: [],
  } as Connection & { accountIdentity: string }
  const review = {
    profile,
    accountIdentity: connection.accountIdentity,
    region: provider.regions![0],
  }
  const plan = {
    id: "plan",
    revision: "reviewed",
    catalog: true,
    executable: true,
    status: "ready",
    projectId: "project",
    manifestId: "manifest",
    environment: "production",
    connections: { [provider.cloud]: connection.id },
    sources: [],
    problems: [],
    warnings: [],
    actions: [
      {
        op: "create",
        subject: "resource",
        provider: target,
        region: review.region,
        name: "database",
        resource: {
          kind: "postgres",
          provider: target,
          version: "17",
          region: review.region,
          managed: {
            profile,
            connectionId: connection.id,
            accountIdentity: review.accountIdentity,
          },
        },
      },
    ],
  } as unknown as Plan
  return { profile, provider, connection, review, plan }
}

// Fixture responses exercise the generated decoder against the API's wire names.
export function managedWire(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(managedWire)
  if (value && typeof value === "object")
    return Object.fromEntries(
      Object.entries(value).map(([key, child]) => [
        key.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`),
        managedWire(child),
      ])
    )
  return value
}
