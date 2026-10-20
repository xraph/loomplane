export const databaseRecipes = {
  postgres: { title: "PostgreSQL", version: "17" },
  mysql: { title: "MySQL", version: "8.4" },
  mariadb: { title: "MariaDB", version: "11.8" },
} as const

export type DatabaseKind = keyof typeof databaseRecipes

export function isDatabaseKind(value: unknown): value is DatabaseKind {
  return typeof value === "string" && Object.hasOwn(databaseRecipes, value)
}
