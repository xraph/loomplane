// How a service reads a database or cache it needs: in one variable with
// the whole connection string, or in separate ones for its host, port,
// user, password and database. What it already reads in its code, as
// analysis found, decides where the form starts.

/** What a wired variable holds. */
export type WiringOutput =
  "url" | "host" | "port" | "user" | "password" | "database"

export const partOutputs: Exclude<WiringOutput, "url">[] = [
  "host",
  "port",
  "user",
  "password",
  "database",
]

export const outputLabels: Record<WiringOutput, string> = {
  url: "Connection string",
  host: "Host",
  port: "Port",
  user: "User",
  password: "Password",
  database: "Database",
}

/** Variables by name, each with what it holds, as loomplane.yaml's wiring.<need>.env. */
export type WiringEnv = Record<string, WiringOutput>

/** The kinds whose address has parts a variable can hold. */
const families: Record<string, "postgres" | "mysql" | "redis" | "mongodb"> = {
  postgres: "postgres",
  mysql: "mysql",
  mariadb: "mysql",
  redis: "redis",
  valkey: "redis",
  mongodb: "mongodb",
}

/** wirable reports whether a kind's variables can be chosen. */
export function wirable(kind?: string): boolean {
  return !!kind && kind in families
}

/** A named set of variables, one per part. */
export type PartTemplate = {
  id: string
  label: string
  vars: Partial<Record<Exclude<WiringOutput, "url">, string>>
}

const familyTemplates: Record<string, PartTemplate> = {
  postgres: {
    id: "libpq",
    label: "PGHOST, PGPORT…",
    vars: {
      host: "PGHOST",
      port: "PGPORT",
      user: "PGUSER",
      password: "PGPASSWORD",
      database: "PGDATABASE",
    },
  },
  mysql: {
    id: "mysql",
    label: "MYSQL_HOST, MYSQL_PORT…",
    vars: {
      host: "MYSQL_HOST",
      port: "MYSQL_PORT",
      user: "MYSQL_USER",
      password: "MYSQL_PASSWORD",
      database: "MYSQL_DATABASE",
    },
  },
  redis: {
    id: "redis",
    label: "REDIS_HOST, REDIS_PORT…",
    vars: {
      host: "REDIS_HOST",
      port: "REDIS_PORT",
      password: "REDIS_PASSWORD",
      database: "REDIS_DB",
    },
  },
  mongodb: {
    id: "mongodb",
    label: "MONGODB_HOST, MONGODB_PORT…",
    vars: {
      host: "MONGODB_HOST",
      port: "MONGODB_PORT",
      user: "MONGODB_USER",
      password: "MONGODB_PASSWORD",
      database: "MONGODB_DATABASE",
    },
  },
}

/** prefixTemplate is DB_HOST, DB_PORT and so on, under any prefix. */
export function prefixTemplate(prefix: string): PartTemplate {
  const p = prefix.toUpperCase().replace(/[^A-Z0-9_]/g, "")
  return {
    id: "prefix",
    label: `${p}HOST, ${p}PORT…`,
    vars: {
      host: `${p}HOST`,
      port: `${p}PORT`,
      user: `${p}USER`,
      password: `${p}PASSWORD`,
      database: `${p}NAME`,
    },
  }
}

/** templatesFor are the ready-made sets for a kind: its engine's own, then a prefix. */
export function templatesFor(kind: string, prefix = "DB_"): PartTemplate[] {
  const family = families[kind]
  return [...(family ? [familyTemplates[family]] : []), prefixTemplate(prefix)]
}

/** The names code commonly reads a whole connection string from, by engine. */
const urlNames: Record<string, string[]> = {
  postgres: [
    "DATABASE_URL",
    "POSTGRES_URL",
    "POSTGRESQL_URL",
    "PG_URL",
    "PG_DSN",
    "DATABASE_DSN",
    "DB_URL",
    "DB_DSN",
    "DATABASE_URI",
  ],
  mysql: [
    "DATABASE_URL",
    "MYSQL_URL",
    "MYSQL_DSN",
    "DATABASE_DSN",
    "DB_URL",
    "DB_DSN",
    "DATABASE_URI",
  ],
  redis: ["REDIS_URL", "REDIS_URI", "REDIS_DSN", "CACHE_URL", "VALKEY_URL"],
  mongodb: [
    "MONGODB_URI",
    "MONGODB_URL",
    "MONGO_URI",
    "MONGO_URL",
    "DATABASE_URL",
  ],
}

/** urlNamesFor are the names a whole connection string is usually read from. */
export function urlNamesFor(kind: string): string[] {
  return urlNames[families[kind] ?? ""] ?? []
}

/** How the form starts for one service. */
export type WiringChoice =
  | { mode: "one"; key: string }
  | {
      mode: "parts"
      template: string
      prefix: string
      vars: Partial<Record<Exclude<WiringOutput, "url">, string>>
    }

/** A starting choice and why. */
export type Suggestion = {
  choice: WiringChoice
  /** The variables the code reads that it was chosen for. */
  detected: string[]
}

/**
 * suggestWiring is where the form starts for a service that reads the
 * names in reads: the engine's separate variables when the code reads two
 * or more of them, else a connection string it reads by one of the usual
 * names, else the kind's own variable.
 */
export function suggestWiring(
  kind: string,
  fallback: string,
  reads: string[]
): Suggestion {
  const read = new Set(reads.map((r) => r.toUpperCase()))
  for (const t of templatesFor(kind)) {
    const found = Object.entries(t.vars).filter(
      ([, name]) => name && read.has(name)
    )
    if (found.length >= 2)
      return {
        choice: {
          mode: "parts",
          template: t.id,
          prefix: "DB_",
          vars: Object.fromEntries(found),
        },
        detected: found.map(([, name]) => name as string),
      }
  }
  const url = urlNamesFor(kind).find((n) => read.has(n))
  if (url) return { choice: { mode: "one", key: url }, detected: [url] }
  return { choice: { mode: "one", key: fallback }, detected: [] }
}

/**
 * envOf is the wiring a choice writes, or undefined when it's the kind's
 * own variable, which needs none.
 */
export function envOf(
  choice: WiringChoice,
  fallback: string
): WiringEnv | undefined {
  if (choice.mode === "one") {
    const key = choice.key.trim()
    return key && key !== fallback ? { [key]: "url" } : undefined
  }
  const env: WiringEnv = {}
  for (const output of partOutputs) {
    const name = choice.vars[output]?.trim()
    if (name) env[name] = output
  }
  return Object.keys(env).length ? env : undefined
}

const variableName = /^[A-Za-z_][A-Za-z0-9_]{0,254}$/

/** wiringProblem is what's wrong with a choice, or "". */
export function wiringProblem(choice: WiringChoice): string {
  const names =
    choice.mode === "one"
      ? [choice.key.trim()]
      : Object.values(choice.vars)
          .map((v) => (v ?? "").trim())
          .filter(Boolean)
  if (names.length === 0) return "Name at least one variable."
  const bad = names.find((n) => !variableName.test(n))
  if (bad !== undefined)
    return `${bad || "An empty name"} isn't a variable name: use letters, digits and underscores, not starting with a digit.`
  const twice = names.find((n, i) => names.indexOf(n) !== i)
  if (twice) return `${twice} is used twice.`
  return ""
}
