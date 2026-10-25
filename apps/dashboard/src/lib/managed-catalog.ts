import type {
  CloudSQLSourceEndpoint,
  ManagedDatabaseProfile,
  Provider,
} from "@/client/forge/loomplane/types"
import type { DatabaseProvider } from "@/lib/catalog-targets"

export type ManagedDatabaseProvider = "rds" | "cloud-sql"
export type ManagedDatabaseReview = {
  profile: ManagedDatabaseProfile
  accountIdentity: string
  region: string
  cloudSqlPsc?: CloudSQLSourceEndpoint
}

export function managedDatabaseAccessLabel(access: string) {
  if (access === "private-vpc") return "Private VPC"
  if (access === "cloud-sql-connector") return "Cloud SQL connector"
  if (access === "cloud-sql-psc-connector")
    return "Private Service Connect (PSC)"
  return access
}

export function sameCloudSQLSourceEndpoint(
  actual: CloudSQLSourceEndpoint | undefined,
  expected: CloudSQLSourceEndpoint | undefined
) {
  if (!expected) return actual === undefined
  return (
    !!actual &&
    actual.consumerProject === expected.consumerProject &&
    actual.network === expected.network &&
    actual.forwardingRule === expected.forwardingRule
  )
}

export function cloudSQLSourceEndpointValid(
  endpoint: CloudSQLSourceEndpoint | undefined,
  region: string
) {
  if (!endpoint) return false
  const projectPattern = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/
  const namePattern = /^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/
  const network = endpoint.network.split("/")
  const rule = endpoint.forwardingRule.split("/")
  return (
    projectPattern.test(endpoint.consumerProject) &&
    namePattern.test(region) &&
    network.length === 5 &&
    network[0] === "projects" &&
    network[1] === endpoint.consumerProject &&
    network[2] === "global" &&
    network[3] === "networks" &&
    namePattern.test(network[4]) &&
    rule.length === 6 &&
    rule[0] === "projects" &&
    rule[1] === endpoint.consumerProject &&
    rule[2] === "regions" &&
    rule[3] === region &&
    rule[4] === "forwardingRules" &&
    namePattern.test(rule[5])
  )
}

export function isManagedDatabaseProvider(
  provider: DatabaseProvider | undefined
): provider is ManagedDatabaseProvider {
  return provider === "rds" || provider === "cloud-sql"
}

export function managedDatabaseCloud(provider: ManagedDatabaseProvider) {
  return provider === "rds" ? "aws" : "gcp"
}

function validManagedProfile(profile: ManagedDatabaseProfile) {
  if (!profile || typeof profile !== "object") return false
  const strings = [
    "id",
    "revision",
    "provider",
    "kind",
    "version",
    "computeClass",
    "storageType",
    "availability",
    "access",
    "backupRetentionUnit",
    "finalBackup",
  ] as const
  const numbers = [
    "storageGb",
    "backupRetentionValue",
    "pointInTimeRecoveryDays",
    "finalBackupRetentionDays",
  ] as const
  return (
    strings.every(
      (key) => typeof profile[key] === "string" && !!profile[key].trim()
    ) &&
    numbers.every(
      (key) => Number.isSafeInteger(profile[key]) && profile[key] >= 0
    ) &&
    profile.storageGb > 0 &&
    profile.backupRetentionValue > 0 &&
    typeof profile.storageEncrypted === "boolean" &&
    typeof profile.retainAutomatedBackups === "boolean" &&
    ["days", "count"].includes(profile.backupRetentionUnit)
  )
}

export function managedDatabaseProfiles(
  providers: Provider[],
  target: DatabaseProvider
) {
  if (!isManagedDatabaseProvider(target)) return []
  const provider = providers.find(
    (p) =>
      p.name === target &&
      p.cloud === managedDatabaseCloud(target) &&
      p.available
  )
  return (
    provider?.provisions
      ?.find((offer) => offer.kind === "postgres")
      ?.managedProfiles?.filter(
        (profile) =>
          validManagedProfile(profile) &&
          profile.provider === target &&
          profile.kind === "postgres" &&
          profile.version === "17"
      ) ?? []
  )
}

export function managedDatabaseProfile(
  providers: Provider[],
  target: DatabaseProvider,
  profileId?: string
) {
  const profiles = managedDatabaseProfiles(providers, target)
  return profiles.find((profile) =>
    profileId
      ? profile.id === profileId
      : profile.access ===
        (target === "cloud-sql" ? "cloud-sql-connector" : "private-vpc")
  )
}

export function sameManagedDatabaseProfile(
  actual: ManagedDatabaseProfile | undefined,
  expected: ManagedDatabaseProfile
) {
  return (
    !!actual &&
    Object.keys(actual).length === Object.keys(expected).length &&
    (Object.keys(expected) as (keyof ManagedDatabaseProfile)[]).every(
      (key) => actual[key] === expected[key]
    )
  )
}
