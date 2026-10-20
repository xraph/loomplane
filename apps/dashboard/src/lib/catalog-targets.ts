import type { Connection, Recipe } from "@/client/forge/loomplane/types"
import type { DatabaseKind } from "@/lib/catalog-recipes"

export type DatabaseProvider = "server" | "kubernetes"

export function databaseTargetProvider(
  connection: Connection,
  kind: DatabaseKind
): DatabaseProvider | undefined {
  const details = connection.details
  if (
    connection.status !== "ready" ||
    !["amd64", "arm64"].includes(details.architecture)
  )
    return
  if (connection.cloud === "server") return "server"
  if (
    connection.cloud === "kubernetes" &&
    kind === "postgres" &&
    details.namespace &&
    details.namespace_uid &&
    details.storage_class &&
    details.storage_class_uid &&
    details.storage_reclaim === "Retain" &&
    details.persistent_storage === "ready" &&
    details.postgres_recovery_permissions === "ready"
  )
    return "kubernetes"
}

// The one-click action authorizes this immutable cluster recipe only. A changed
// image, capacity or recovery format returns the plan to ordinary review.
export function isKubernetesPostgresRecipe(recipe: Recipe) {
  const images: Record<string, string> = {
    amd64:
      "docker.io/library/postgres@sha256:aa90e97ee862e558111d34cfb8b2c4bec768c2b039fb791341686928560263b3",
    arm64:
      "docker.io/library/postgres@sha256:0b2882c46a2b8d8e148431b333394348cf4d7f15aec6df7f17d51dc641e56cfa",
  }
  return (
    recipe.id === "postgresql-17-kubernetes" &&
    recipe.revision === "1" &&
    recipe.productVersion === "17.11" &&
    recipe.image === images[recipe.architecture] &&
    recipe.mountPath === "/var/lib/postgresql/data" &&
    recipe.backupFormat === "pg-dump-custom-v1" &&
    recipe.volumeSizeMb === 10240
  )
}

export function sameDatabasePlacement(
  actual: Record<string, unknown> | undefined,
  expected: Record<string, unknown>
) {
  return (
    !!actual &&
    [
      "endpoint",
      "cluster_ca",
      "context",
      "namespace",
      "namespace_uid",
      "architecture",
      "storage_class",
      "storage_class_uid",
      "storage_provisioner",
      "storage_binding",
      "storage_reclaim",
    ].every((key) => actual[key] === expected[key])
  )
}
