import type {
  ManagedDatabaseProfile,
  Provider,
} from "@/client/forge/loomplane/types"
import type { DatabaseProvider } from "@/lib/catalog-targets"

export type ManagedDatabaseProvider = "rds" | "cloud-sql"
export type ManagedDatabaseReview = {
  profile: ManagedDatabaseProfile
  accountIdentity: string
  region: string
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

export function managedDatabaseProfile(
  providers: Provider[],
  target: DatabaseProvider
) {
  if (!isManagedDatabaseProvider(target)) return
  const provider = providers.find(
    (p) =>
      p.name === target &&
      p.cloud === managedDatabaseCloud(target) &&
      p.available
  )
  return provider?.provisions
    ?.find((offer) => offer.kind === "postgres")
    ?.managedProfiles?.find(
      (profile) =>
        validManagedProfile(profile) &&
        profile.provider === target &&
        profile.kind === "postgres" &&
        profile.version === "17"
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
