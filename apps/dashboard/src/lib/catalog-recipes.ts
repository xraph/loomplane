const databaseStorage =
  "Private endpoint, restricted application role and a retained local volume. Host loss needs a backup stored elsewhere."
const cacheStorage =
  "Private endpoint, AOF persistence every second and native RDB snapshots. The dataset is limited to 128 MiB without eviction. Keep backups outside this host. Recovery copies are read-only. This profile has no failover."

export const databaseRecipes = {
  postgres: {
    title: "PostgreSQL",
    version: "17",
    description: databaseStorage,
  },
  mysql: { title: "MySQL", version: "8.4", description: databaseStorage },
  mariadb: { title: "MariaDB", version: "11.8", description: databaseStorage },
  redis: { title: "Redis", version: "8.2", description: cacheStorage },
  valkey: { title: "Valkey", version: "8.1", description: cacheStorage },
} as const

export type DatabaseKind = keyof typeof databaseRecipes

export function isDatabaseKind(value: unknown): value is DatabaseKind {
  return typeof value === "string" && Object.hasOwn(databaseRecipes, value)
}
